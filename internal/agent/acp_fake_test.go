package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The ACP tests run the test binary itself as a fake agent: started with
// "-fake-acp script log", it plays the recorded traffic in script (one
// step per line, see fakeACP) and writes every message it gets to log.
func TestMain(m *testing.M) {
	if len(os.Args) >= 4 && os.Args[1] == "-fake-acp" {
		os.Exit(fakeACP(os.Args[2], os.Args[3]))
	}
	os.Exit(m.Run())
}

// fakeStep is one step of a fake agent's script:
//
//	{"recv":"session/new","result":{…}}  read the next message: a request or
//	                                     notification with this method; answer
//	                                     at once with result or error, if given
//	{"reply":"session/prompt","result":…} answer the last such request now
//	{"send":{…}}                         write a message (an update, a request)
//	{"await":100}                        read the next message: the answer to
//	                                     request 100
//
// After the last step it reads until its input ends. A message other than
// the one expected is written to log as "UNEXPECTED …" and the agent exits 3.
type fakeStep struct {
	Recv   string          `json:"recv"`
	Reply  string          `json:"reply"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Send   json.RawMessage `json:"send"`
	Await  json.RawMessage `json:"await"`
}

func fakeACP(script, logPath string) int {
	f, err := os.Open(script)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer f.Close()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer log.Close()
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	read := func() (acpMessage, bool) {
		if !in.Scan() {
			return acpMessage{}, false
		}
		fmt.Fprintln(log, in.Text())
		var m acpMessage
		_ = json.Unmarshal(in.Bytes(), &m)
		return m, true
	}
	write := func(v any) {
		b, _ := json.Marshal(v)
		os.Stdout.Write(append(b, '\n'))
	}
	answer := func(id json.RawMessage, s fakeStep) {
		msg := map[string]any{"jsonrpc": "2.0", "id": id}
		if len(s.Error) > 0 {
			msg["error"] = s.Error
		} else if len(s.Result) == 0 || string(s.Result) == "null" {
			msg["result"] = map[string]any{}
		} else {
			msg["result"] = s.Result
		}
		write(msg)
	}
	ids := map[string]json.RawMessage{} // method → id of its last request
	steps := bufio.NewScanner(f)
	steps.Buffer(make([]byte, 1<<20), 16<<20)
	for steps.Scan() {
		var s fakeStep
		if err := json.Unmarshal(steps.Bytes(), &s); err != nil {
			fmt.Fprintln(log, "BAD STEP", err)
			return 2
		}
		switch {
		case s.Recv != "":
			m, ok := read()
			if !ok || m.Method != s.Recv {
				fmt.Fprintln(log, "UNEXPECTED: wanted", s.Recv)
				return 3
			}
			if len(m.ID) > 0 {
				ids[m.Method] = m.ID
				if len(s.Result) > 0 || len(s.Error) > 0 {
					answer(m.ID, s)
				}
			}
		case s.Reply != "":
			answer(ids[s.Reply], s)
		case len(s.Send) > 0:
			os.Stdout.Write(append([]byte(strings.TrimSpace(string(s.Send))), '\n'))
		case len(s.Await) > 0:
			m, ok := read()
			if !ok || m.Method != "" || string(m.ID) != string(s.Await) {
				fmt.Fprintln(log, "UNEXPECTED: wanted the answer to", string(s.Await))
				return 3
			}
		}
	}
	for {
		if _, ok := read(); !ok {
			return 0
		}
	}
}
