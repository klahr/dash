package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// An environment's policy: who may deploy to it, approve its deploys and
// change its settings (the org roles: owner > admin > member, see
// store.Role), what a deploy needs first (passing tests, being live on
// another environment) and when nothing deploys (freezes). The API
// enforces it; rollbacks need only the right to deploy.

// The roles an environment's policy names (store.Role's values), and
// RoleNone: no approval needed.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleNone   = "none"
)

// policyRoles are the roles a policy may name, lowest first.
var policyRoles = []string{RoleMember, RoleAdmin, RoleOwner}

// RoleRank orders the policy roles (0: none of them).
func RoleRank(role string) int { return slices.Index(policyRoles, role) + 1 }

// Production reports whether e is, by its name, a production environment:
// its defaults are stricter, and nothing is promoted from it.
func (e Environment) Production() bool { return e.Name == DefaultEnvironment || e.Name == "prod" }

// WhoDeploys is the lowest role that may deploy to e (a rollback too):
// DeployRole, by default admin for production and member elsewhere.
func (e Environment) WhoDeploys() string {
	switch {
	case RoleRank(e.DeployRole) > 0:
		return e.DeployRole
	case e.Production():
		return RoleAdmin
	}
	return RoleMember
}

// WhoApproves is the lowest role that must approve a deploy to e before it
// runs, "" when none must: ApproveRole, by default admin for production
// and none elsewhere.
func (e Environment) WhoApproves() string {
	switch {
	case e.ApproveRole == RoleNone:
		return ""
	case RoleRank(e.ApproveRole) > 0:
		return e.ApproveRole
	case e.Production():
		return RoleAdmin
	}
	return ""
}

// WhoChangesSettings is the lowest role that may change e's settings
// (admin by default).
func (e Environment) WhoChangesSettings() string {
	if RoleRank(e.SettingsRole) > 0 {
		return e.SettingsRole
	}
	return RoleAdmin
}

// WithDefaults is e with its roles spelled out (what "" stands for).
func (e Environment) WithDefaults() Environment {
	e.DeployRole, e.SettingsRole = e.WhoDeploys(), e.WhoChangesSettings()
	if e.ApproveRole = e.WhoApproves(); e.ApproveRole == "" {
		e.ApproveRole = RoleNone
	}
	return e
}

// cleanPolicy checks e's roles, requirements and freezes, filling in the
// default roles and putting freezes in their stored form.
func (e *Environment) cleanPolicy() error {
	for _, r := range []struct {
		what string
		v    *string
		none bool
	}{{"who may deploy", &e.DeployRole, false}, {"who must approve", &e.ApproveRole, true}, {"who may change settings", &e.SettingsRole, false}} {
		*r.v = strings.ToLower(strings.TrimSpace(*r.v))
		if *r.v != "" && RoleRank(*r.v) == 0 && !(r.none && *r.v == RoleNone) {
			return fmt.Errorf("%s must be member, admin or owner", r.what)
		}
	}
	*e = e.WithDefaults()
	e.RequireLiveOn = strings.ToLower(strings.TrimSpace(e.RequireLiveOn))
	e.TimeZone = strings.TrimSpace(e.TimeZone)
	if e.TimeZone != "" {
		if _, err := time.LoadLocation(e.TimeZone); err != nil || strings.EqualFold(e.TimeZone, "local") {
			return fmt.Errorf("unknown time zone %q (use a name such as Europe/Stockholm)", e.TimeZone)
		}
	}
	if len(e.Freezes) > MaxFreezes {
		return fmt.Errorf("at most %d freeze windows", MaxFreezes)
	}
	for i := range e.Freezes {
		f, err := e.Freezes[i].clean()
		if err != nil {
			return fmt.Errorf("freeze window %d: %v", i+1, err)
		}
		e.Freezes[i] = f
	}
	if len(e.Freezes) == 0 {
		e.Freezes = nil
	}
	return nil
}

// MaxFreezes caps an environment's freeze windows.
const MaxFreezes = 20

// Freeze is a time nothing deploys to an environment: every week, from a
// day and time to another ("fri 15:00" → "mon 00:00"), or once, from a
// date and time to another ("2026-12-23 12:00" → "2027-01-02 00:00"), in
// the environment's time zone. Note says why.
type Freeze struct {
	From string `json:"from"`
	To   string `json:"to"`
	Note string `json:"note,omitempty"`
}

var (
	weeklyRe = regexp.MustCompile(`^(?i)(mon|tue|wed|thu|fri|sat|sun)[a-z]*\s+(\d{1,2}):(\d{2})$`)
	weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

const dateLayout = "2006-01-02 15:04"

// weekly parses "fri 15:00": its weekday and minutes into the day.
func weekly(s string) (time.Weekday, int, bool) {
	m := weeklyRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	if h > 24 || mi > 59 || h == 24 && mi != 0 {
		return 0, 0, false
	}
	return time.Weekday(slices.Index(weekdays, strings.ToLower(m[1]))), h*60 + mi, true
}

// Weekly reports whether f repeats every week.
func (f Freeze) Weekly() bool { _, _, ok := weekly(f.From); return ok }

func (f Freeze) clean() (Freeze, error) {
	f.From, f.To, f.Note = strings.Join(strings.Fields(f.From), " "), strings.Join(strings.Fields(f.To), " "), strings.TrimSpace(f.Note)
	if len(f.Note) > 200 {
		return f, errors.New("the note is longer than 200 characters")
	}
	fd, fm, fok := weekly(f.From)
	td, tm, tok := weekly(f.To)
	if fok && tok {
		if fd == td && fm == tm {
			return f, errors.New("it starts and ends at the same time")
		}
		f.From = fmt.Sprintf("%s %02d:%02d", weekdays[fd], fm/60, fm%60)
		f.To = fmt.Sprintf("%s %02d:%02d", weekdays[td], tm/60, tm%60)
		return f, nil
	}
	from, err1 := time.Parse(dateLayout, f.From)
	to, err2 := time.Parse(dateLayout, f.To)
	if err1 != nil || err2 != nil {
		return f, errors.New(`give "from" and "to" as a weekday and time ("fri 15:00", "mon 07:00") or as dates ("2026-12-23 12:00")`)
	}
	if !to.After(from) {
		return f, errors.New("it must end after it starts")
	}
	return f, nil
}

// Location is where e's freezes are read (UTC when it names none, or an
// unknown one).
func (e Environment) Location() *time.Location {
	if e.TimeZone != "" {
		if loc, err := time.LoadLocation(e.TimeZone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// FrozenAt is the freeze of e that is on at now, if any, and when it ends.
func (e Environment) FrozenAt(now time.Time) (Freeze, time.Time, bool) {
	loc := e.Location()
	t := now.In(loc)
	for _, f := range e.Freezes {
		if until, ok := f.on(t, loc); ok {
			return f, until, true
		}
	}
	return Freeze{}, time.Time{}, false
}

// on reports whether f is on at t (in loc), and when it ends.
func (f Freeze) on(t time.Time, loc *time.Location) (time.Time, bool) {
	fd, fm, fok := weekly(f.From)
	td, tm, tok := weekly(f.To)
	if fok && tok {
		const week = 7 * 24 * 60
		at := int(t.Weekday())*24*60 + t.Hour()*60 + t.Minute()
		from, to := int(fd)*24*60+fm, int(td)*24*60+tm
		if (at-from+week)%week >= (to-from+week)%week {
			return time.Time{}, false
		}
		days := (int(td) - int(t.Weekday()) + 7) % 7
		until := time.Date(t.Year(), t.Month(), t.Day()+days, 0, tm, 0, 0, loc)
		if !until.After(t) {
			until = until.AddDate(0, 0, 7)
		}
		return until, true
	}
	from, err1 := time.ParseInLocation(dateLayout, f.From, loc)
	to, err2 := time.ParseInLocation(dateLayout, f.To, loc)
	if err1 != nil || err2 != nil || t.Before(from) || !t.Before(to) {
		return time.Time{}, false
	}
	return to, true
}

// FreezeReason says why e takes no deploys at now ("" when it does).
func (e Environment) FreezeReason(now time.Time) string {
	f, until, ok := e.FrozenAt(now)
	if !ok {
		return ""
	}
	zone := e.TimeZone
	if zone == "" {
		zone = "UTC"
	}
	msg := fmt.Sprintf("%s is frozen until %s (%s)", e.Name, until.Format("Mon 2 Jan 15:04"), zone)
	if f.Note != "" {
		msg += ": " + f.Note
	}
	return msg
}

// DeployApproval is who approved a deploy, or rejected it, and when.
type DeployApproval struct {
	Approved bool      `json:"approved"`
	By       int64     `json:"by,omitempty"`
	ByName   string    `json:"by_name,omitempty"`
	At       time.Time `json:"at"`
	Note     string    `json:"note,omitempty"`
}

// ---- settings history ----

// EnvChange is one change to one of a project's environments, kept as its
// settings history: who made it, when, and each setting before and after.
type EnvChange struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	// Action is EnvAdded, EnvChanged or EnvRemoved.
	Action string           `json:"action"`
	Diff   []EnvFieldChange `json:"diff"`
	By     int64            `json:"by,omitempty"`
	ByName string           `json:"by_name,omitempty"`
	At     time.Time        `json:"at"`
}

// EnvChange.Action.
const (
	EnvAdded   = "added"
	EnvChanged = "changed"
	EnvRemoved = "removed"
)

// EnvFieldChange is one setting of an environment, before and after
// ("" for none), by its JSON name ("deploy_command").
type EnvFieldChange struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// DiffEnvironments are the changes from before to after, one per
// environment added, changed or removed, in after's order (removed ones
// last). Defaults count as set (an old record without its roles spelled
// out is the same as one with them).
func DiffEnvironments(before, after []Environment) []EnvChange {
	old := map[string]Environment{}
	for _, e := range before {
		old[e.Name] = e
	}
	var out []EnvChange
	kept := map[string]bool{}
	for _, e := range after {
		kept[e.Name] = true
		was, ok := old[e.Name]
		if !ok {
			out = append(out, EnvChange{Environment: e.Name, Action: EnvAdded, Diff: diffEnv(Environment{}, e.WithDefaults())})
			continue
		}
		if d := diffEnv(was.WithDefaults(), e.WithDefaults()); len(d) > 0 {
			out = append(out, EnvChange{Environment: e.Name, Action: EnvChanged, Diff: d})
		}
	}
	for _, e := range before {
		if !kept[e.Name] {
			out = append(out, EnvChange{Environment: e.Name, Action: EnvRemoved, Diff: diffEnv(e.WithDefaults(), Environment{})})
		}
	}
	return out
}

// diffEnv lists the settings that differ between a and b, in the order
// they are declared.
func diffEnv(a, b Environment) []EnvFieldChange {
	am, bm := envFields(a), envFields(b)
	var out []EnvFieldChange
	for _, k := range envFieldOrder() {
		if k == "name" {
			continue
		}
		if am[k] != bm[k] {
			out = append(out, EnvFieldChange{Field: k, From: am[k], To: bm[k]})
		}
	}
	return out
}

// envFields are e's settings as text, by JSON name ("" for unset).
func envFields(e Environment) map[string]string {
	b, _ := json.Marshal(e)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	out := map[string]string{}
	for k, v := range raw {
		var s string
		var fs []Freeze
		switch {
		case k == "freezes" && json.Unmarshal(v, &fs) == nil:
			parts := make([]string, len(fs))
			for i, f := range fs {
				parts[i] = f.From + " → " + f.To
				if f.Note != "" {
					parts[i] += " (" + f.Note + ")"
				}
			}
			s = strings.Join(parts, "; ")
		case json.Unmarshal(v, &s) == nil:
		case string(v) == "false" || string(v) == "0" || string(v) == "null" || string(v) == "[]":
		default:
			var list []string
			if json.Unmarshal(v, &list) == nil {
				s = strings.Join(list, ", ")
			} else {
				s = string(v)
			}
		}
		out[k] = s
	}
	return out
}

// envFieldOrder is Environment's JSON names, as declared.
func envFieldOrder() []string {
	t := reflect.TypeOf(Environment{})
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	return out
}
