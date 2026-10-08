package acp

import (
	"encoding/json"
	"testing"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// ADR-0078: an ACP session gets the gateway's MCP server only with a grant, a
// key and an agent that speaks MCP over HTTP; otherwise it gets none.
func TestMCPServersOnlyTheGatewayAndOnlyWhenGranted(t *testing.T) {
	granted := harness.LLMEnv{APIKey: "ploeg-isolated-placeholder", MCPURL: "http://127.0.0.1:41234/mcp"}
	httpCaps := sdk.McpCapabilities{Http: true}
	for name, tc := range map[string]struct {
		llm  harness.LLMEnv
		caps sdk.McpCapabilities
	}{
		"no grant":         {harness.LLMEnv{APIKey: "k"}, httpCaps},
		"no key":           {harness.LLMEnv{MCPURL: granted.MCPURL}, httpCaps},
		"agent lacks http": {granted, sdk.McpCapabilities{Sse: true}},
	} {
		if got := mcpServers(tc.llm, tc.caps); got == nil || len(got) != 0 {
			t.Errorf("%s: servers = %+v, want an empty list", name, got)
		}
	}
	got := mcpServers(granted, httpCaps)
	if len(got) != 1 || got[0].Http == nil {
		t.Fatalf("servers = %+v, want one HTTP server", got)
	}
	body, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		URL     string `json:"url"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Type != "http" || wire.Name != "litellm" || wire.URL != granted.MCPURL ||
		len(wire.Headers) != 1 || wire.Headers[0].Name != "x-litellm-api-key" || wire.Headers[0].Value != "Bearer ploeg-isolated-placeholder" {
		t.Fatalf("server on the wire = %s", body)
	}
}
