package model

import "slices"

// The kinds of a project's shell commands (Project.CommandsBy).
const (
	CmdRun     = "run"
	CmdTest    = "test"
	CmdDeploy  = "deploy"
	CmdSetup   = "setup"
	CmdPreview = "preview"
)

// CommandKinds are every kind of project command.
var CommandKinds = []string{CmdRun, CmdTest, CmdDeploy, CmdSetup, CmdPreview}

// CommandSetter is who last set one of a hub project's commands.
type CommandSetter struct {
	User int64 `json:"user"`
	// Admin: they were an org admin (or owner) then. An admin's command
	// runs on every runner of the project.
	Admin bool `json:"admin,omitempty"`
	// Name is their username then, for messages.
	Name string `json:"name,omitempty"`
	// Approved are the members who saved it again unchanged since: it
	// runs on their own runners too.
	Approved []int64 `json:"approved,omitempty"`
}

// Saved records that user (admin: an org admin) saved a project's command
// of kind: changed (a new command) or unchanged (approving it). It returns
// the new record of who set it.
func (by CommandSetter) Saved(user int64, name string, admin, changed bool) CommandSetter {
	if changed {
		return CommandSetter{User: user, Admin: admin, Name: name}
	}
	switch {
	case admin:
		by.Admin = true
	case user != by.User && !slices.Contains(by.Approved, user):
		by.Approved = append(slices.Clone(by.Approved), user)
	}
	return by
}

// Command is p's command of kind ("" for none).
func (p Project) Command(kind string) string {
	switch kind {
	case CmdRun:
		return p.RunCommand
	case CmdTest:
		return p.TestCommand
	case CmdDeploy:
		return p.DeployCommand
	case CmdSetup:
		return p.SetupCommand
	case CmdPreview:
		return p.PreviewCommand
	}
	return ""
}

// SetCommand sets p's command of kind.
func (p *Project) SetCommand(kind, cmd string) {
	switch kind {
	case CmdRun:
		p.RunCommand = cmd
	case CmdTest:
		p.TestCommand = cmd
	case CmdDeploy:
		p.DeployCommand = cmd
	case CmdSetup:
		p.SetupCommand = cmd
	case CmdPreview:
		p.PreviewCommand = cmd
	}
}

// CommandApproved reports whether a hub project's command of kind may run
// on a runner owned by owner (shared: the runner takes other members'
// work, with an API key and no subscription login): it predates
// CommandsBy, or an org admin or owner set it, or the runner is shared.
func (p Project) CommandApproved(kind string, owner int64, shared bool) bool {
	by, ok := p.CommandsBy[kind]
	return !ok || shared || by.Admin || (owner > 0 && (by.User == owner || slices.Contains(by.Approved, owner)))
}

// Held is, on a runner, why p's command of kind may not run here ("": it
// may).
func (p Project) Held(kind string) string { return p.HeldCommands[kind] }
