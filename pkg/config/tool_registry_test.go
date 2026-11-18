package config

import (
	"strings"
	"testing"
)

func TestToolRegistryLoadsStaticServersSourcesAndTeamTools(t *testing.T) {
	f, err := Load(write(t, `
toolRegistry:
  enabled: true
  mcpServers:
    obs:
      url: https://mcp.example.test/mcp
      auth: {kind: header, header: X-Api-Key}
      credentialEnv: MCP_OBS_KEY
      tools: [{name: search_logs, access: read}]
  skillSources:
    review:
      kind: oci
      ref: registry.example.test/skills/review@sha256:`+strings.Repeat("a", 64)+`
teams:
  bronze:
    tools:
      "*": {mcp: [{server: obs}], skills: [review]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if s := f.ToolRegistry.MCPServers["obs"]; s.Auth.Header != "X-Api-Key" || s.Tools[0].Name != "search_logs" {
		t.Fatalf("%+v", s)
	}
	if rt := f.Teams["bronze"].Tools["*"]; rt.MCP[0].Server != "obs" || rt.Skills[0] != "review" {
		t.Fatalf("%+v", rt)
	}
}

func TestToolRegistryRefusesUnsafeStaticEntries(t *testing.T) {
	for name, body := range map[string]string{
		"stdio":         "toolRegistry:\n  mcpServers:\n    x: {url: https://x.example.test/mcp, transport: stdio, auth: {kind: none}, tools: [{name: a, access: read}]}\n",
		"write tool":    "toolRegistry:\n  mcpServers:\n    x: {url: https://x.example.test/mcp, auth: {kind: none}, tools: [{name: a, access: write}]}\n",
		"no credential": "toolRegistry:\n  mcpServers:\n    x: {url: https://x.example.test/mcp, auth: {kind: bearer}, tools: [{name: a, access: read}]}\n",
		"oci tag":       "toolRegistry:\n  skillSources:\n    x: {kind: oci, ref: registry.example.test/skills/x:latest}\n",
		"tools no reg":  "teams:\n  bronze:\n    tools: {\"*\": {skills: [x]}}\n",
		"bad env":       "toolRegistry:\n  mcpServers:\n    x: {url: https://x.example.test/mcp, auth: {kind: bearer}, credentialEnv: lower, tools: [{name: a, access: read}]}\n",
		"user info url": "toolRegistry:\n  mcpServers:\n    x: {url: 'https://u:p@x.example.test/mcp', auth: {kind: none}, tools: [{name: a, access: read}]}\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
