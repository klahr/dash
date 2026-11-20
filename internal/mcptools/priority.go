package mcptools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// A task's priority is a number: prioritised tasks run first, lower before
// higher, and unprioritised ones (no number) after all of them. The board's
// forms offer it as levels over that number, which the API and the tools
// also take: None (no number), High (0 and up) and Urgent (below 0).
const (
	PriorityHigh   = 0
	PriorityUrgent = -1
)

// priorityLevels is the help for a priority the API or a tool refused.
const priorityLevels = `use a whole number (lower runs first), or "none", "high" (0) or "urgent" (-1)`

// ParsePriority reads a task priority as JSON decodes it into an any: nil or
// "none" is no priority (nil), "high" 0, "urgent" -1, and a whole number (or
// a string of one) itself.
func ParsePriority(v any) (*int, error) {
	var n int
	switch x := v.(type) {
	case nil:
		return nil, nil
	case float64:
		if x != math.Trunc(x) || math.Abs(x) > math.MaxInt32 {
			return nil, fmt.Errorf("priority %v: %s", x, priorityLevels)
		}
		n = int(x)
	case int:
		n = x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		switch s {
		case "none", "":
			return nil, nil
		case "high":
			n = PriorityHigh
		case "urgent":
			n = PriorityUrgent
		default:
			i, err := strconv.Atoi(s)
			if err != nil || i > math.MaxInt32 || i < math.MinInt32 {
				return nil, fmt.Errorf("unknown priority %q: %s", x, priorityLevels)
			}
			n = i
		}
	default:
		return nil, fmt.Errorf("priority %v: %s", v, priorityLevels)
	}
	return &n, nil
}

// Priority is a task priority in a JSON body: absent (Set false), or Set to
// one ParsePriority reads, Value nil for null or "none".
type Priority struct {
	Set   bool
	Value *int
}

func (p *Priority) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	n, err := ParsePriority(v)
	if err != nil {
		return err
	}
	p.Set, p.Value = true, n
	return nil
}

// OptPriority is the priority argument k, or nil when it wasn't passed or
// is null or "none" (see ParsePriority).
func (a Args) OptPriority(k string) (*int, error) {
	return ParsePriority(a[k])
}
