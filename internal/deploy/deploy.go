// Package deploy runs a deploy where the project's code is: on a runner (or
// a Dash that isn't a hub), in a checkout of the commit it deploys. It runs
// the environment's build command, then its deploy command, then checks
// the environment's URL, and keeps the output with the secrets in it masked.
//
// The hub keeps the record of every deploy (model.Deploy) and never sees a
// secret: each runner keeps an environment's secrets in a file of its own,
// ~/.dash/env/<environment>.env (KEY=VALUE lines), passed to the commands
// as environment variables. The hub only learns which environments a
// runner has a file for (EnvFiles), so it can warn when none has.
package deploy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// EnvDir is the folder of the environments' secrets files: ~/.dash/env.
func EnvDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".dash", "env")
}

// EnvFiles lists the environments dir has a secrets file for (<name>.env),
// sorted. A missing folder has none.
func EnvFiles(dir string) []string {
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".env")
		if ok && !e.IsDir() && model.ValidEnvName(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ReadEnvFile reads environment env's secrets file in dir: KEY=VALUE lines
// (an "export " before the key, and quotes around the value, are allowed;
// blank lines and # comments are skipped). No file is no secrets.
func ReadEnvFile(dir, env string) ([]string, error) {
	if dir == "" || !model.ValidEnvName(env) {
		return nil, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, env+".env"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !validKey(k) {
			return nil, fmt.Errorf("%s.env line %d: want KEY=VALUE", env, n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out = append(out, k+"="+v)
	}
	return out, sc.Err()
}

func validKey(k string) bool {
	if k == "" || k[0] >= '0' && k[0] <= '9' {
		return false
	}
	for _, c := range k {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// minSecret is the shortest value masked: shorter ones ("1", "on") would
// mask ordinary output and give nothing away.
const minSecret = 4

// Masked is what a secret's value becomes in a log.
const Masked = "***"

// Mask replaces, in s, every value of the KEY=VALUE secrets.
func Mask(s string, secrets []string) string {
	vals := make([]string, 0, len(secrets))
	for _, kv := range secrets {
		if _, v, _ := strings.Cut(kv, "="); len(v) >= minSecret {
			vals = append(vals, v)
		}
	}
	// The longest first, so one value inside another is masked whole.
	slices.SortFunc(vals, func(a, b string) int { return len(b) - len(a) })
	for _, v := range vals {
		s = strings.ReplaceAll(s, v, Masked)
	}
	return s
}

// ParseOutputs reads a script's $DASH_OUTPUT file: key=value lines, of
// which only model.DeployOutputs are kept (the last of each), each value
// cut to a line of 500 bytes.
func ParseOutputs(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		k = strings.ToLower(strings.TrimSpace(k))
		if !ok || !slices.Contains(model.DeployOutputs, k) {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) > 500 {
			v = v[:500]
		}
		out[k] = v
	}
	return out
}

// Job is one deploy to run.
type Job struct {
	ID string
	// Dir is where the commands run: a checkout of the commit deployed.
	Dir string
	// Build and Deploy are the environment's commands (Build optional).
	Build, Deploy string
	// Env is the commands' environment: this process's (or a shared
	// runner's), the secrets and the DASH_* variables (model.DeployVar…).
	// DASH_OUTPUT is added.
	Env []string
	// Secrets are the KEY=VALUE pairs of the secrets file, masked in the log.
	Secrets []string
	// URL is checked once the deploy command is done ("": not checked),
	// unless the script reports another.
	URL string
}

// State is how a job is doing.
type State struct {
	ID string `json:"id"`
	// Phase is model.DeployBuilding, DeployDeploying or DeployVerifying
	// while it runs, then DeployLive or DeployFailed.
	Phase    model.DeployStatus `json:"phase"`
	Running  bool               `json:"running"`
	ExitCode *int               `json:"exit_code,omitempty"`
	Error    string             `json:"error,omitempty"`
	// Log is its output so far, secrets masked, the newest
	// model.MaxDeployLog of it.
	Log string `json:"log"`
	// Outputs are what the script wrote to $DASH_OUTPUT.
	Outputs map[string]string `json:"outputs,omitempty"`
	// URL is the one checked: the script's, else the job's.
	URL string `json:"url,omitempty"`
}

// Manager runs jobs, one at a time per Key (an environment of a project),
// and remembers finished ones for a while so their state can be read.
type Manager struct {
	// Verify checks a URL after a deploy (default: GET answers below 500
	// within a minute or so).
	Verify func(ctx context.Context, url string) error
	// Keep is how long a finished job is remembered (default an hour).
	Keep time.Duration

	mu   sync.Mutex
	jobs map[string]*job
	busy map[string]string // key → the id of the job running there
}

type job struct {
	key     string
	secrets []string
	cancel  context.CancelFunc

	mu    sync.Mutex
	state State
	log   []byte
	done  time.Time
}

// ErrBusy is returned for a job whose key already has one running.
var ErrBusy = errors.New("a deploy to this environment is already running here")

// ErrNotFound is returned for a job the manager doesn't know (any more).
var ErrNotFound = errors.New("no such deploy on this runner")

// Start runs j in the background under key. ctx bounds it (Stop ends it
// sooner).
func (m *Manager) Start(ctx context.Context, key string, j Job) error {
	if err := localrun.Check("deploy: " + j.Deploy); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if m.jobs == nil {
		m.jobs, m.busy = map[string]*job{}, map[string]string{}
	}
	if _, ok := m.jobs[j.ID]; ok {
		return nil // started already: the hub asked again
	}
	if id, ok := m.busy[key]; ok && id != j.ID {
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &job{key: key, secrets: j.Secrets, cancel: cancel, state: State{ID: j.ID, Running: true, URL: j.URL}}
	m.jobs[j.ID] = r
	m.busy[key] = j.ID
	go func() {
		defer cancel()
		m.run(ctx, r, j)
		m.mu.Lock()
		if m.busy[key] == j.ID {
			delete(m.busy, key)
		}
		m.mu.Unlock()
	}()
	return nil
}

// State is job id's state.
func (m *Manager) State(id string) (State, error) {
	m.mu.Lock()
	r, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return State{}, ErrNotFound
	}
	return r.snapshot(), nil
}

// Stop ends job id (its commands are killed); it fails.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	r, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	r.cancel()
	return nil
}

func (m *Manager) pruneLocked() {
	keep := m.Keep
	if keep <= 0 {
		keep = time.Hour
	}
	for id, r := range m.jobs {
		r.mu.Lock()
		old := !r.done.IsZero() && time.Since(r.done) > keep
		r.mu.Unlock()
		if old {
			delete(m.jobs, id)
		}
	}
}

func (r *job) snapshot() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state
	// Masked whole, then cut: a secret the cut would split is gone by then.
	st.Log = Mask(string(r.log), r.secrets)
	if len(st.Log) > model.MaxDeployLog {
		st.Log = st.Log[len(st.Log)-model.MaxDeployLog:]
	}
	if st.ExitCode != nil {
		c := *st.ExitCode
		st.ExitCode = &c
	}
	if st.Outputs != nil {
		cp := make(map[string]string, len(st.Outputs))
		for k, v := range st.Outputs {
			cp[k] = Mask(v, r.secrets)
		}
		st.Outputs = cp
	}
	return st
}

// Write takes the commands' output, keeping the newest MaxDeployLog (and
// a little more, so a secret cut at the edge is still whole when masked).
func (r *job) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, b...)
	if over := len(r.log) - model.MaxDeployLog - 4096; over > 0 {
		r.log = r.log[:copy(r.log, r.log[over:])]
	}
	return len(b), nil
}

func (r *job) phase(p model.DeployStatus) {
	r.mu.Lock()
	r.state.Phase = p
	r.mu.Unlock()
}

// say adds a line of Dash's own to the log, after a blank line unless it
// is the first.
func (r *job) say(format string, a ...any) {
	r.mu.Lock()
	gap := "\n"
	if len(r.log) == 0 {
		gap = ""
	}
	r.mu.Unlock()
	_, _ = fmt.Fprintf(r, gap+"[dash] "+format+"\n", a...)
}

func (m *Manager) run(ctx context.Context, r *job, j Job) {
	finish := func(p model.DeployStatus, code *int, msg string) {
		r.mu.Lock()
		r.state.Phase, r.state.Running, r.state.ExitCode, r.state.Error = p, false, code, msg
		r.done = time.Now()
		r.mu.Unlock()
	}
	out, err := os.CreateTemp("", "dash-deploy-output-*")
	if err != nil {
		finish(model.DeployFailed, nil, "make the $DASH_OUTPUT file: "+err.Error())
		return
	}
	outPath := out.Name()
	out.Close()
	defer os.Remove(outPath)
	env := append(slices.Clone(j.Env), model.DeployVarOutput+"="+outPath)

	steps := []struct {
		phase model.DeployStatus
		cmd   string
	}{{model.DeployBuilding, j.Build}, {model.DeployDeploying, j.Deploy}}
	for _, st := range steps {
		if st.cmd == "" {
			continue
		}
		r.phase(st.phase)
		r.say("%s: %s", st.phase, firstLine(st.cmd))
		code, err := runScript(ctx, j.Dir, st.cmd, env, r)
		if ctx.Err() != nil {
			r.say("stopped")
			finish(model.DeployFailed, code, "stopped before it finished")
			return
		}
		if err != nil {
			finish(model.DeployFailed, code, err.Error())
			return
		}
		if *code != 0 {
			what := "the deploy command"
			if st.phase == model.DeployBuilding {
				what = "the build command"
			}
			r.say("%s exited with %d", what, *code)
			finish(model.DeployFailed, code, fmt.Sprintf("%s exited with %d", what, *code))
			return
		}
	}
	zero := 0
	outs := map[string]string{}
	if b, err := os.ReadFile(outPath); err == nil {
		outs = ParseOutputs(b)
	}
	url := j.URL
	if u := outs["url"]; strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		url = u
	}
	r.mu.Lock()
	r.state.Outputs, r.state.URL = outs, url
	r.mu.Unlock()
	if url != "" {
		r.phase(model.DeployVerifying)
		r.say("verifying: %s", url)
		verify := m.Verify
		if verify == nil {
			verify = CheckURL
		}
		if err := verify(ctx, url); err != nil {
			r.say("%v", err)
			finish(model.DeployFailed, &zero, "verifying "+url+": "+err.Error())
			return
		}
		r.say("%s answers", url)
	}
	finish(model.DeployLive, &zero, "")
}

func firstLine(s string) string {
	line, rest, _ := strings.Cut(s, "\n")
	if rest != "" {
		line += " …"
	}
	return line
}

// runScript runs a bash script in dir with env, its output to w, and
// returns its exit code. Ending ctx kills it and what it started.
func runScript(ctx context.Context, dir, script string, env []string, w *job) (*int, error) {
	cmd := exec.CommandContext(ctx, localrun.Bash(), "-lc", script)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, w, w
	localrun.Group(cmd, syscall.SIGTERM, 10*time.Second)
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		code = -1
		return &code, fmt.Errorf("run it: %w", err)
	}
	return &code, nil
}

// CheckURL checks that url answers below 500, trying for about a minute
// (a server that restarts takes a moment).
func CheckURL(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	var last error
	for i := 0; i < 12; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		res.Body.Close()
		if res.StatusCode < 500 {
			return nil
		}
		last = fmt.Errorf("it answers %s", res.Status)
	}
	return last
}
