package claudecli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Stream checks one run's stream-json output, line by line, against what
// Dash reads from it. Get one from Monitor.Stream; a nil Monitor is fine.
type Stream struct {
	m      *Monitor
	seen   []string
	init   bool
	result bool
	lines  int
}

// Stream starts checking a run's output. Its warnings also go to m.
func (m *Monitor) Stream() *Stream { return &Stream{m: m} }

// Start returns the monitor's warnings so far (from its last Check and
// earlier runs), for the top of the run's log; Line and End won't repeat them.
func (s *Stream) Start() []string {
	ws := s.m.Warnings()
	s.seen = append(s.seen, ws...)
	return ws
}

// Line checks one line of stdout and returns warnings this run hasn't
// reported yet.
func (s *Stream) Line(line string) []string {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	s.lines++
	typ, ws := checkLine(line)
	switch typ {
	case "system":
		s.init = true // only init is checked; other system events come and go
	case "result":
		s.result = true
	}
	return s.add(ws)
}

// End is called when the run exited cleanly. It returns warnings about what
// the run should have printed but didn't.
func (s *Stream) End() []string {
	if s.lines == 0 {
		return nil // printed nothing at all: the caller reports that already
	}
	var ws []string
	if !s.init {
		ws = append(ws, "stream-json: no system event before the conversation (Dash takes the session id from it)")
	}
	if !s.result {
		ws = append(ws, "stream-json: the run ended without a result event (Dash reads the outcome, cost and turns from it)")
	}
	return s.add(ws)
}

func (s *Stream) add(ws []string) []string {
	var out []string
	for _, w := range ws {
		if !slices.Contains(s.seen, w) {
			s.seen = append(s.seen, w)
			out = append(out, w)
		}
	}
	s.m.observed(out)
	return out
}

// Known stream-json event types. Others are ignored without a warning:
// Claude Code adds new ones from time to time and Dash doesn't need them.
var knownTypes = []string{"system", "assistant", "user", "result", "rate_limit_event", "stream_event"}

// checkLine checks one stream-json line: that it's an event, and that the
// events Dash reads have the fields it reads, with the expected types. It
// returns the event's type ("" when it has none, or a known type it
// recognised) and what doesn't look as expected.
func checkLine(line string) (string, []string) {
	var ev map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &ev) != nil {
		return "", []string{"stream-json: a line that isn't a JSON object: " + oneLine(line)}
	}
	var typ string
	if json.Unmarshal(ev["type"], &typ) != nil || typ == "" {
		return "", []string{"stream-json: an event without a \"type\": " + oneLine(line)}
	}
	if !slices.Contains(knownTypes, typ) {
		return typ, nil
	}
	c := checker{typ: typ}
	switch typ {
	case "system":
		var sub string
		_ = json.Unmarshal(ev["subtype"], &sub)
		if sub != "init" {
			return "", nil // status updates and the like: not read by Dash
		}
		c.typ = "system init"
		c.want(ev, "session_id", kindString)
	case "assistant", "user":
		var msg map[string]json.RawMessage
		if !c.want(ev, "message", kindObject) || json.Unmarshal(ev["message"], &msg) != nil {
			break
		}
		if typ == "user" && kindOf(msg["content"]) == kindString {
			break // a plain prompt
		}
		if !c.want(msg, "content", kindArray) {
			break
		}
		var blocks []map[string]json.RawMessage
		_ = json.Unmarshal(msg["content"], &blocks)
		for _, b := range blocks {
			var bt string
			_ = json.Unmarshal(b["type"], &bt)
			switch bt {
			case "text":
				c.want(b, "text", kindString)
			case "tool_use":
				c.want(b, "id", kindString)
				c.want(b, "name", kindString)
				c.want(b, "input", kindObject)
			case "tool_result":
				c.want(b, "tool_use_id", kindString)
				if k := kindOf(b["content"]); k != kindString && k != kindArray && k != kindNone {
					c.bad = append(c.bad, "tool_result content is "+string(k))
				}
			case "":
				c.bad = append(c.bad, "a content block without a type")
			}
		}
	case "result":
		c.want(ev, "subtype", kindString)
		c.want(ev, "is_error", kindBool)
		c.want(ev, "session_id", kindString)
		c.want(ev, "num_turns", kindNumber)
		c.want(ev, "total_cost_usd", kindNumber)
		var sub string
		_ = json.Unmarshal(ev["subtype"], &sub)
		if sub == "success" {
			c.want(ev, "result", kindString)
		}
		if kindOf(ev["usage"]) != kindNone {
			c.want(ev, "usage", kindObject)
		}
	case "rate_limit_event":
		var info map[string]json.RawMessage
		if !c.want(ev, "rate_limit_info", kindObject) || json.Unmarshal(ev["rate_limit_info"], &info) != nil {
			break
		}
		c.typ = "rate_limit_info"
		c.want(info, "status", kindString)
		switch {
		case kindOf(info["unifiedWindows"]) == kindObject:
			var ws map[string]map[string]json.RawMessage
			if json.Unmarshal(info["unifiedWindows"], &ws) != nil {
				c.bad = append(c.bad, "unifiedWindows isn't a map of windows")
				break
			}
			for _, name := range sortedKeys(ws) {
				w := ws[name]
				for _, f := range []string{"utilization", "resetsAt"} {
					if kindOf(w[f]) != kindNumber {
						c.bad = append(c.bad, fmt.Sprintf("unifiedWindows.%s.%s %s", name, f, describe(kindOf(w[f]), kindNumber)))
					}
				}
				if kindOf(w["status"]) != kindString {
					c.bad = append(c.bad, fmt.Sprintf("unifiedWindows.%s.status %s", name, describe(kindOf(w["status"]), kindString)))
				}
			}
		case kindOf(info["rateLimitType"]) == kindString:
			c.want(info, "resetsAt", kindNumber) // the older, single-window form
		default:
			c.bad = append(c.bad, "neither unifiedWindows nor rateLimitType (the queue can't see the usage windows)")
		}
	case "stream_event":
		var e map[string]json.RawMessage
		if !c.want(ev, "event", kindObject) || json.Unmarshal(ev["event"], &e) != nil {
			break
		}
		c.typ = "stream_event"
		if !c.want(e, "type", kindString) {
			break
		}
		var et string
		_ = json.Unmarshal(e["type"], &et)
		if et == "content_block_delta" && c.want(e, "delta", kindObject) {
			var d map[string]json.RawMessage
			_ = json.Unmarshal(e["delta"], &d)
			var dt string
			_ = json.Unmarshal(d["type"], &dt)
			switch dt {
			case "text_delta":
				c.want(d, "text", kindString)
			case "thinking_delta":
				c.want(d, "thinking", kindString)
			}
		}
	}
	if len(c.bad) == 0 {
		return typ, nil
	}
	return typ, []string{fmt.Sprintf("stream-json %s: %s", c.typ, strings.Join(c.bad, "; "))}
}

type kind string

const (
	kindNone   kind = "missing"
	kindNull   kind = "null"
	kindString kind = "a string"
	kindNumber kind = "a number"
	kindBool   kind = "a boolean"
	kindObject kind = "an object"
	kindArray  kind = "an array"
)

func kindOf(raw json.RawMessage) kind {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return kindNone
	}
	switch s[0] {
	case '"':
		return kindString
	case '{':
		return kindObject
	case '[':
		return kindArray
	case 't', 'f':
		return kindBool
	case 'n':
		return kindNull
	}
	return kindNumber
}

func describe(got, want kind) string {
	if got == kindNone {
		return "is missing"
	}
	return fmt.Sprintf("is %s, not %s", got, want)
}

// checker collects what's wrong with one event.
type checker struct {
	typ string
	bad []string
}

// want checks that obj[field] is of kind k.
func (c *checker) want(obj map[string]json.RawMessage, field string, k kind) bool {
	if got := kindOf(obj[field]); got != k {
		c.bad = append(c.bad, field+" "+describe(got, k))
		return false
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}
