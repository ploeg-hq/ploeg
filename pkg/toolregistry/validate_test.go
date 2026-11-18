package toolregistry

import (
	"bytes"
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func publicResolver(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
}

func privateResolver(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
}

func goodServer() ServerInput {
	return ServerInput{Key: "observability", Name: "Logs and metrics", URL: "https://mcp.example.test/mcp", Transport: TransportStreamableHTTP,
		Auth: Auth{Kind: AuthBearer}, Tools: []Tool{{Name: "search_logs", Access: AccessRead}}, HasCredential: true, CredentialLen: 10}
}

func TestCheckServerRefusesEachUnsafeField(t *testing.T) {
	p := Policy{Resolve: publicResolver}
	if e := p.CheckServer(context.Background(), goodServer()); e != nil {
		t.Fatalf("a good server is refused: %v", e)
	}
	cases := map[string]struct {
		mutate func(*ServerInput)
		policy Policy
		code   string
	}{
		"stdio":                  {func(s *ServerInput) { s.Transport = TransportStdio }, p, "transport_needs_adapter"},
		"user info":              {func(s *ServerInput) { s.URL = "https://user:pw@mcp.example.test/mcp" }, p, "invalid_url"},
		"fragment":               {func(s *ServerInput) { s.URL = "https://mcp.example.test/mcp#x" }, p, "invalid_url"},
		"plain http":             {func(s *ServerInput) { s.URL = "http://mcp.example.test/mcp" }, p, "host_not_allowed"},
		"file scheme":            {func(s *ServerInput) { s.URL = "file:///etc/passwd" }, p, "invalid_url"},
		"private host":           {func(s *ServerInput) {}, Policy{Resolve: privateResolver}, "host_not_allowed"},
		"loopback literal":       {func(s *ServerInput) { s.URL = "https://127.0.0.1/mcp" }, p, "host_not_allowed"},
		"metadata address":       {func(s *ServerInput) { s.URL = "https://169.254.169.254/latest" }, p, "host_not_allowed"},
		"credential on none":     {func(s *ServerInput) { s.Auth.Kind = AuthNone }, p, "credential_not_allowed"},
		"no credential":          {func(s *ServerInput) { s.HasCredential = false }, p, "credential_required"},
		"write tool not allowed": {func(s *ServerInput) { s.Tools = []Tool{{Name: "delete", Access: AccessWrite}} }, p, "write_tools_not_allowed"},
		"cookie header":          {func(s *ServerInput) { s.Auth = Auth{Kind: AuthHeader, Header: "Cookie"} }, p, "invalid_request"},
		"mcp header":             {func(s *ServerInput) { s.Auth = Auth{Kind: AuthHeader, Header: "Mcp-Session-Id"} }, p, "invalid_request"},
		"duplicate tool":         {func(s *ServerInput) { s.Tools = append(s.Tools, s.Tools[0]) }, p, "invalid_request"},
		"no tools":               {func(s *ServerInput) { s.Tools = nil }, p, "invalid_request"},
		"bad key":                {func(s *ServerInput) { s.Key = "Bad_Key" }, p, "invalid_request"},
		"oauth over http": {func(s *ServerInput) {
			s.Auth = Auth{Kind: AuthOAuthClientCredentials, ClientID: "c", TokenURL: "http://idp/t"}
		}, p, "invalid_url"},
	}
	for name, c := range cases {
		in := goodServer()
		c.mutate(&in)
		e := c.policy.CheckServer(context.Background(), in)
		if e == nil || e.Code != c.code {
			t.Errorf("%s: got %v, want %s", name, e, c.code)
		}
	}
}

func TestListedHostsLiftThePrivateAndPlainHTTPRefusals(t *testing.T) {
	in := goodServer()
	in.URL = "http://mcp-adapter.tools.svc:8080/mcp"
	p := Policy{Resolve: privateResolver, PlainHTTPHosts: []string{"mcp-adapter.tools.svc"}}
	if e := p.CheckServer(context.Background(), in); e != nil {
		t.Fatalf("a listed adapter host is refused: %v", e)
	}
	in.URL = "https://grafana.internal/mcp"
	p = Policy{Resolve: privateResolver, PrivateHosts: []string{"grafana.internal"}}
	if e := p.CheckServer(context.Background(), in); e != nil {
		t.Fatalf("a listed private host is refused: %v", e)
	}
}

func TestWriteToolsNeedTheInstallationsOptIn(t *testing.T) {
	in := goodServer()
	in.Tools = []Tool{{Name: "create_issue", Access: AccessWrite}}
	if e := (Policy{Resolve: publicResolver, AllowWriteTools: true}).CheckServer(context.Background(), in); e != nil {
		t.Fatalf("allowWriteTools still refuses: %v", e)
	}
}

func TestCheckSourceAcceptsOnlyDigestPins(t *testing.T) {
	p := Policy{Resolve: publicResolver}
	digest := "sha256:" + strings.Repeat("a", 64)
	good := []SourceInput{
		{Key: "s", Name: "s", Kind: SourceOCI, Ref: "registry.example.test/skills/review@" + digest},
		{Key: "s", Name: "s", Kind: SourceHTTPS, URL: "https://cdn.example.test/skills.tar.gz", Digest: digest},
		{Key: "s", Name: "s", Kind: SourceGit, URL: "https://git.example.test/skills.git", Commit: strings.Repeat("b", 40), Digest: digest},
	}
	for _, in := range good {
		if e := p.CheckSource(context.Background(), in); e != nil {
			t.Errorf("%s: %v", in.Kind, e)
		}
	}
	bad := map[string]SourceInput{
		"oci tag":      {Key: "s", Name: "s", Kind: SourceOCI, Ref: "registry.example.test/skills/review:latest"},
		"https no pin": {Key: "s", Name: "s", Kind: SourceHTTPS, URL: "https://cdn.example.test/skills.tar.gz"},
		"git branch":   {Key: "s", Name: "s", Kind: SourceGit, URL: "https://git.example.test/skills.git", Commit: "main", Digest: digest},
	}
	for name, in := range bad {
		if e := p.CheckSource(context.Background(), in); e == nil || e.Code != "digest_required" {
			t.Errorf("%s: got %v, want digest_required", name, e)
		}
	}
}

func registry() ([]Server, []SkillSource) {
	servers := []Server{{ID: "mcp_01jd8x4k2v6r9t3m5n7p0q2s4w", Key: "obs", Tools: []Tool{{Name: "search_logs", Access: AccessRead}, {Name: "silence_alert", Access: AccessWrite}}, State: "active"}}
	sources := []SkillSource{{ID: "skl_01jd8x4k2v6r9t3m5n7p0q2s4w", Key: "review", BundleDigest: "sha256:" + strings.Repeat("c", 64), Skills: []string{"review"}, State: "active"}}
	return servers, sources
}

func TestAttachmentRefusesAWriteToolOnAReaderRoleAndResolvesKeysToIDs(t *testing.T) {
	servers, sources := registry()
	team := Team{Key: "bronze", Roles: []TeamRole{{Name: "builder", Writes: true}, {Name: "reviewer"}}}
	_, e := CheckAttachment(team, map[string]RoleTools{"reviewer": {MCP: []ServerUse{{Server: "obs", Tools: []string{"silence_alert"}}}}}, servers, sources)
	if e == nil || e.Code != "write_tool_on_reader" {
		t.Fatalf("got %v, want write_tool_on_reader", e)
	}
	_, e = CheckAttachment(team, map[string]RoleTools{"*": {MCP: []ServerUse{{Server: "obs", Tools: []string{"silence_alert"}}}}}, servers, sources)
	if e == nil || e.Code != "write_tool_on_reader" {
		t.Fatalf("\"*\" with a reader Role: got %v, want write_tool_on_reader", e)
	}
	out, e := CheckAttachment(team, map[string]RoleTools{"builder": {MCP: []ServerUse{{Server: "obs"}}, Skills: []string{"review"}}}, servers, sources)
	if e != nil {
		t.Fatal(e)
	}
	if got := out["builder"]; got.MCP[0].Server != servers[0].ID || got.Skills[0] != sources[0].ID || len(got.MCP[0].Tools) != 2 {
		t.Fatalf("keys not resolved to ids: %+v", got)
	}
	for name, roles := range map[string]map[string]RoleTools{
		"unknown_role":   {"planner": {}},
		"unknown_server": {"builder": {MCP: []ServerUse{{Server: "nope"}}}},
		"unknown_tool":   {"builder": {MCP: []ServerUse{{Server: "obs", Tools: []string{"drop_database"}}}}},
		"unknown_source": {"builder": {Skills: []string{"nope"}}},
	} {
		if _, e := CheckAttachment(team, roles, servers, sources); e == nil || e.Code != name {
			t.Errorf("got %v, want %s", e, name)
		}
	}
}

func TestBuildGrantMergesStarAndLeavesWriteToolsOutOfAReader(t *testing.T) {
	servers, sources := registry()
	att := Attachment{Roles: map[string]RoleTools{
		"*":       {MCP: []ServerUse{{Server: servers[0].ID, Tools: []string{"search_logs", "silence_alert"}}}, Skills: []string{sources[0].ID}},
		"builder": {MCP: []ServerUse{{Server: servers[0].ID, Tools: []string{"search_logs"}}}},
	}}
	reader := BuildGrant(att, TeamRole{Name: "reviewer"}, servers, sources)
	if len(reader.Servers) != 1 || len(reader.Servers[0].Tools) != 1 || reader.Servers[0].Tools["search_logs"] != AccessRead {
		t.Fatalf("reader grant: %+v", reader.Servers)
	}
	writer := BuildGrant(att, TeamRole{Name: "builder", Writes: true}, servers, sources)
	if writer.Servers[0].Tools["silence_alert"] != AccessWrite || len(writer.Skills) != 1 || writer.Skills[0].Digest != sources[0].BundleDigest {
		t.Fatalf("writer grant: %+v", writer)
	}
	servers[0].Tools = servers[0].Tools[:1]
	narrowed := BuildGrant(att, TeamRole{Name: "builder", Writes: true}, servers, sources)
	if _, still := narrowed.Servers[0].Tools["silence_alert"]; still {
		t.Fatal("a tool the server no longer allows stays in the grant")
	}
}

func TestNewIDIsAPrefixedLowercaseULID(t *testing.T) {
	id, err := NewID("mcp", time.UnixMilli(1_700_000_000_000), bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if err != nil || !IsID(id) {
		t.Fatalf("%q %v", id, err)
	}
}
