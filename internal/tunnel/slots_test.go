package tunnel

import (
	"testing"

	"github.com/klahr/dash/internal/usage"
)

// SlotsNow is the runner's limit, or more while the hub's pacing boosts
// it to catch up with the weekly pace.
func TestRunnerSlotsNowCountsTheBoost(t *testing.T) {
	t.Parallel()
	withPace := func(p usage.Pace) Runner {
		return Runner{Parallel: 3, Reported: &Reported{Parallel: 2, Usage: &usage.Snapshot{Known: true, Pace: p}}}
	}
	cases := []struct {
		name string
		r    Runner
		want int
	}{
		{"no report", Runner{Parallel: 3}, 3},
		{"no usage", Runner{Reported: &Reported{Parallel: 2}}, 2},
		{"on pace", withPace(usage.Pace{State: usage.PaceOnPace, Slots: 3}), 3},
		{"catching up", withPace(usage.Pace{State: usage.PaceCatching, Slots: 5}), 5},
		{"catching up, fewer", withPace(usage.Pace{State: usage.PaceCatching, Slots: 1}), 3},
		{"waiting", withPace(usage.Pace{State: usage.PaceWaiting, Slots: 9}), 3},
	}
	for _, c := range cases {
		if got := c.r.SlotsNow(); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
