package mcptools

import "encoding/json"

// ToolName is how Claude Code names tool on MCP server server, in
// --allowedTools and permission rules.
func ToolName(server, tool string) string { return "mcp__" + server + "__" + tool }

// AllowedToolArgs are the claude arguments that allow tools on server
// without asking: "--allowedTools", name, … for each one.
func AllowedToolArgs(server string, tools ...string) []string {
	out := make([]string, 0, 2*len(tools))
	for _, t := range tools {
		out = append(out, "--allowedTools", ToolName(server, t))
	}
	return out
}

// ConfigArgs are the claude arguments that give a run the MCP servers
// servers (name → server config); none when there are none.
func ConfigArgs(servers map[string]any) []string {
	if len(servers) == 0 {
		return nil
	}
	cfg, _ := json.Marshal(map[string]any{"mcpServers": servers})
	return []string{"--mcp-config", string(cfg)}
}
