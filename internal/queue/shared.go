package queue

import (
	"errors"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// SharedWork is how a runner runs its hub's work for other members than
// its owner (model.Todo.SharedWork): Claude and the project's commands run
// with the runner's API key and none of its owner's logins.
type SharedWork struct {
	// Dir is the Claude config folder of that work (CLAUDE_CONFIG_DIR):
	// not the owner's, so Claude can't fall back to their login.
	Dir string
	// Check says why the runner may not do other members' work now (nil:
	// it may): its hub turned Take other members' tasks off, or it no
	// longer runs Claude with an API key alone. Every step checks it.
	Check func() error
}

// errNoSharedWork is why other members' work doesn't run on a runner that
// was never set up for it.
var errNoSharedWork = errors.New("this task is for another member than this runner's owner, and this runner doesn't take other members' work")

// ok says why t, other members' work, may not go on here now (nil: it may,
// or t is its owner's).
func (s *SharedWork) ok(t model.Todo) error {
	if !t.SharedWork {
		return nil
	}
	if s == nil || s.Dir == "" || s.Check == nil {
		return errNoSharedWork
	}
	return s.Check()
}

// shellEnv is the environment of a project command run for t: nil (this
// process's, see runShell) for its owner's work, SharedEnv for others'.
func (s *SharedWork) shellEnv(t model.Todo) ([]string, error) {
	if !t.SharedWork {
		return nil, nil
	}
	if err := s.ok(t); err != nil {
		return nil, err
	}
	return claudecli.SharedEnv(localrun.Environ("CI=1"), s.Dir), nil
}

// claudeDir is the Claude config folder of t's runs ("": the owner's).
func (s *SharedWork) claudeDir(t model.Todo) (string, error) {
	if !t.SharedWork {
		return "", nil
	}
	if err := s.ok(t); err != nil {
		return "", err
	}
	return s.Dir, nil
}

// heldErr is why p's command of kind doesn't run here (nil: it may), on a
// runner whose hub held it (model.Project.HeldCommands).
func heldErr(p model.Project, kind string) error {
	if why := p.Held(kind); why != "" {
		return errors.New(why)
	}
	return nil
}
