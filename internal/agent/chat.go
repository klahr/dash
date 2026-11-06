package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/klahr/dash/internal/model"
)

const DefaultModel = "claude-opus-5-5"

// ChatMessage is the wire format the frontend sends: the full conversation so far.
type ChatMessage = model.ChatMessage

// ChatDelta is one streamed fragment sent back to the frontend.
type ChatDelta struct {
	Kind string `json:"kind"` // "thinking" | "text" | "done" | "error"
	Text string `json:"text,omitempty"`
}

// Chat answers through the Anthropic Messages API (billed to an API key).
// See ClaudeCodeChat for subscription billing.
type Chat struct {
	client anthropic.Client
	Model  string
	System string
}

// NewChat uses the SDK's default credential chain (ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN, or an `ant auth login` profile).
func NewChat(opts ...option.RequestOption) *Chat {
	return &Chat{
		client: anthropic.NewClient(opts...),
		Model:  DefaultModel,
		System: (&Chat{}).defaultSystem(),
	}
}

func (*Chat) defaultSystem() string {
	return "You are the assistant inside Dash, an AI-native development environment. " +
		"Answer questions about the user's projects and code concisely."
}

// Stream sends the conversation to Claude and calls emit for every delta.
func (c *Chat) Stream(ctx context.Context, _ string, history []ChatMessage, emit func(ChatDelta)) error {
	msgs := make([]anthropic.BetaMessageParam, 0, len(history))
	for _, m := range history {
		block := anthropic.NewBetaTextBlock(m.Content)
		switch m.Role {
		case "user":
			msgs = append(msgs, anthropic.NewBetaUserMessage(block))
		case "assistant":
			msgs = append(msgs, anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{
				Content: []anthropic.BetaContentBlockParamUnion{block},
			}})
		default:
			return fmt.Errorf("unknown role %q", m.Role)
		}
	}

	stream := c.client.Beta.Messages.NewStreaming(ctx, anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.Model),
		MaxTokens: 64000,
		System:    []anthropic.BetaTextBlockParam{{Text: c.System}},
		Messages:  msgs,
		Thinking: anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
			Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
		}},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
		// Server-side refusal fallback: if a safety classifier declines, the
		// API re-serves the request on a fallback model within the same call.
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	defer stream.Close()

	for stream.Next() {
		switch ev := stream.Current().AsAny().(type) {
		case anthropic.BetaRawContentBlockDeltaEvent:
			switch d := ev.Delta.AsAny().(type) {
			case anthropic.BetaTextDelta:
				emit(ChatDelta{Kind: "text", Text: d.Text})
			case anthropic.BetaThinkingDelta:
				emit(ChatDelta{Kind: "thinking", Text: d.Thinking})
			}
		case anthropic.BetaRawMessageDeltaEvent:
			if ev.Delta.StopReason == anthropic.BetaStopReasonRefusal {
				emit(ChatDelta{Kind: "error", Text: "Claude declined this request."})
			}
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401, 403:
				return fmt.Errorf("anthropic auth failed (set ANTHROPIC_API_KEY or run `ant auth login`): %w", err)
			case 429:
				return fmt.Errorf("rate limited, try again shortly: %w", err)
			}
		}
		return err
	}
	emit(ChatDelta{Kind: "done"})
	return nil
}
