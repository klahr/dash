package agent

import (
	"strconv"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/spend"
)

// streamCost estimates a Claude Code run's cost as it streams, from the
// usage of each assistant message (Claude Code reports the run's real cost
// only in its result event, at the end). A message streams as several
// events with the same id, each with its usage so far: the latest counts.
type streamCost struct {
	msgs  map[string]model.ChatUsage
	cost  map[string]float64
	total float64
	anon  int
}

// add takes the usage of message id (of model m) and returns the run's
// estimated cost so far.
func (c *streamCost) add(id, m string, u model.ChatUsage) float64 {
	if c.msgs == nil {
		c.msgs, c.cost = map[string]model.ChatUsage{}, map[string]float64{}
	}
	if id == "" {
		c.anon++
		id = "#" + strconv.Itoa(c.anon)
	}
	usd, _ := spend.Estimate(m, false, u)
	c.total += usd - c.cost[id]
	c.msgs[id], c.cost[id] = u, usd
	return c.total
}

// usage is the tokens of every message so far.
func (c *streamCost) usage() model.ChatUsage {
	var sum model.ChatUsage
	for _, u := range c.msgs {
		sum.InputTokens += u.InputTokens
		sum.OutputTokens += u.OutputTokens
		sum.CacheReadTokens += u.CacheReadTokens
		sum.CacheCreationTokens += u.CacheCreationTokens
	}
	return sum
}
