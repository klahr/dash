// Package spend estimates what agent runs cost from their tokens and adds
// up what a project spent, for its spending caps (model.Spending) and its
// spend trend (docs/spending.md).
package spend

import (
	"strings"

	"github.com/klahr/dash/internal/model"
)

// Price is a model's list price in US dollars per million tokens.
type Price struct {
	Input, Output, CacheRead, CacheWrite float64
}

// price is input and output per million tokens, cache reads at read; cache
// writes cost 1.25 times the input, as Anthropic's 5-minute cache does.
func price(input, output, read float64) Price {
	return Price{Input: input, Output: output, CacheRead: read, CacheWrite: input * 1.25}
}

// claudePrices are Anthropic's list prices (Nov 2026), by a part of the
// model id, most specific first. Claude Code reports a run's cost itself
// at the end; these only estimate it while the run streams.
var claudePrices = []struct {
	match string
	price Price
}{
	{"fable-5-2", price(8, 40, 0.20)},
	{"mythos-5-2", price(8, 40, 0.20)},
	{"fable", price(10, 50, 1)},
	{"mythos", price(10, 50, 1)},
	{"opus-5-5", price(4, 20, 0.20)},
	{"opus-5", price(5, 25, 0.50)},
	{"opus-4-8", price(5, 25, 0.50)},
	{"opus-4-7", price(5, 25, 0.50)},
	{"opus-4-6", price(5, 25, 0.50)},
	{"opus-4-5", price(5, 25, 0.50)},
	{"opus-4", price(15, 75, 1.50)}, // Opus 4 and 4.1
	{"opus", price(4, 20, 0.20)},    // the alias: the current Opus
	{"sonnet-5-5", price(2, 10, 0.10)},
	{"sonnet-5", price(2, 10, 0.20)},
	{"sonnet-4", price(3, 15, 0.30)},
	{"sonnet", price(2, 10, 0.10)},
	{"haiku-5-5", price(0.10, 0.50, 0.01)},
	{"haiku-4-5", price(1, 5, 0.10)},
	{"haiku-3-5", price(0.80, 4, 0.08)},
	{"haiku", price(0.10, 0.50, 0.01)},
}

// claudeDefault prices a Claude model the table doesn't know: as the
// current Opus, Claude Code's usual model.
var claudeDefault = price(4, 20, 0.20)

// openAIPrices are OpenAI's list prices for the models Codex runs on, by a
// part of the model id, most specific first. Codex reports tokens, never a
// cost: these make its runs' cost an estimate.
var openAIPrices = []struct {
	match string
	price Price
}{
	{"o4-mini", Price{Input: 1.10, Output: 4.40, CacheRead: 0.275}},
	{"nano", Price{Input: 0.05, Output: 0.40, CacheRead: 0.005}},
	{"mini", Price{Input: 0.25, Output: 2, CacheRead: 0.025}},
	{"o3", Price{Input: 2, Output: 8, CacheRead: 0.50}},
	{"gpt-4.1", Price{Input: 2, Output: 8, CacheRead: 0.50}},
	{"gpt-5", Price{Input: 1.25, Output: 10, CacheRead: 0.125}}, // gpt-5, gpt-5-codex, gpt-5.x
}

// codexDefault prices a Codex run on its default model ("") or one the
// table doesn't know: as GPT-5.
var codexDefault = Price{Input: 1.25, Output: 10, CacheRead: 0.125}

// PriceOf is the price of model m: a Claude model's when m names one (or
// codex is false), else an OpenAI model's.
func PriceOf(m string, codex bool) Price {
	l := strings.ToLower(m)
	if strings.Contains(l, "claude") || !codex {
		for _, p := range claudePrices {
			if strings.Contains(l, p.match) {
				return p.price
			}
		}
		if codex && l == "" {
			return codexDefault
		}
		return claudeDefault
	}
	for _, p := range openAIPrices {
		if strings.Contains(l, p.match) {
			return p.price
		}
	}
	return codexDefault
}

// Cost is what u costs at p. InputTokens don't include cached ones (as
// Dash keeps them, model.ChatUsage).
func (p Price) Cost(u model.ChatUsage) float64 {
	return (float64(u.InputTokens)*p.Input + float64(u.OutputTokens)*p.Output +
		float64(u.CacheReadTokens)*p.CacheRead + float64(u.CacheCreationTokens)*p.CacheWrite) / 1e6
}

// Estimate is what tokens u of model m cost, as Dash estimates it; codex
// says the run was Codex's. ok is false when u has no tokens at all: then
// nothing is known, and nothing counts.
func Estimate(m string, codex bool, u model.ChatUsage) (usd float64, ok bool) {
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 {
		return 0, false
	}
	return PriceOf(m, codex).Cost(u), true
}
