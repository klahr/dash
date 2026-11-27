package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// AutoReview is Claude's review of finished task t for the project's
// auto-approve rules (queue.TodoEngine.Review): the review the task chat's
// Review button asks for (model.AutoReviewPrompt), run here in t's worktree,
// read-only as nobody is there to approve a tool call. The question and
// the answer are added to the task's chat, where the user reads them as a
// review they asked for. It returns the answer.
func (s *Server) AutoReview(ctx context.Context, t model.Todo, p model.Project) (string, error) {
	if s.Chat == nil {
		return "", errChatUnavailable
	}
	chatter := s.chatFor(p)
	if t.Worktree == nil {
		return "", errors.New("the task has no worktree to review")
	}
	diff, err := s.todoChanges(ctx, p, t)
	if err != nil {
		return "", err
	}
	asked := agent.ChatMessage{Role: "user", Content: model.AutoReviewPrompt(p), At: ptr(now())}
	prompt := withTodoContext(t, diff, []agent.ChatMessage{asked})
	ctx = agent.WithProject(agent.ReadOnly(ctx), p.ID)
	var reply strings.Builder
	var usage *model.ChatUsage
	var failed error
	started := now()
	err = chatter.Stream(ctx, t.Worktree.Path, prompt, func(d agent.ChatDelta) {
		switch d.Kind {
		case "text":
			reply.WriteString(d.Text)
		case "usage":
			usage = d.Usage
		case "error":
			failed = errors.New(d.Text)
		}
	})
	if err == nil {
		err = failed
	}
	if err != nil {
		return "", err
	}
	answer := reply.String()
	if strings.TrimSpace(answer) == "" {
		return "", errors.New("the review came back empty")
	}
	if org, ok := s.Store.TodoOrg(t.ID); ok {
		data := s.Store.ForOrg(org)
		prev, _ := data.TodoChat(t.ID)
		msgs := append(prev, asked, model.ChatMessage{Role: "assistant", Content: answer, At: &started, Usage: usage})
		if err := data.SetTodoChat(t.ID, msgs); err != nil {
			slog.Warn("auto-approve review: keep it in the task's chat", "todo", t.ID, "err", err)
		}
	}
	return answer, nil
}
