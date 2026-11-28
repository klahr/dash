package agent

// GeminiProvider is the registered name of the Gemini CLI provider.
const GeminiProvider = "gemini"

// GeminiKeyEnv is the API key a Gemini run for another member than the
// runner's owner must use (see ExecRequest.Shared).
const GeminiKeyEnv = "GEMINI_API_KEY"

// Gemini is the ACP profile of Google's Gemini CLI, `gemini --acp`
// (checked against @google/gemini-cli 0.71, Nov 2026):
//
//   - With the client's fs capability off it reads and writes files
//     itself, and it takes MCP servers over HTTP: no `dash mcp-proxy`.
//   - It offers session/load (not session/resume or fork): a task's own
//     session continues; a follow-up task starts fresh.
//   - Its modes are default (asks for edits and commands), autoEdit (asks
//     for commands), yolo and plan. Tasks run in autoEdit, as Claude Code's
//     run with acceptEdits: Dash is asked about commands.
//   - An MCP tool call's server and tool are in its _meta["gemini-cli"].mcp.
//   - Signed out, session/new answers auth_required. The "gemini-api-key"
//     method signs in with GEMINI_API_KEY; GEMINI_CLI_HOME moves ~/.gemini.
//   - Its usage_update has tokens, not cost: Dash can't estimate it.
var Gemini = &ACPProfile{
	Name:           GeminiProvider,
	Title:          "Gemini CLI",
	Bin:            "gemini",
	Args:           []string{"--acp"},
	Install:        "install it with `npm install -g @google/gemini-cli`",
	Login:          "gemini",
	KeyEnv:         GeminiKeyEnv,
	KeyAuthMethod:  "gemini-api-key",
	HomeEnv:        "GEMINI_CLI_HOME",
	TaskMode:       "autoEdit",
	UnattendedMode: "default",
	ChatMode:       "default",
	AddDirFlag:     "--include-directories",
}

func init() { RegisterACPProfile(Gemini) }
