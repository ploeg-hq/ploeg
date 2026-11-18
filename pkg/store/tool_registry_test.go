package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

func toolTenant(t *testing.T) string {
	return "tr-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
}

func toolID(prefix string, n int) string {
	return prefix + "_01jd8x4k2v6r9t3m5n7p0q2s" + string(rune('a'+n)) + "w"
}

func TestToolRegistryServersAreIdempotentByKeyStaleByRevisionAndCarryNoValue(t *testing.T) {
	ctx := context.Background()
	tenant := toolTenant(t)
	audit := ToolAudit{Attribution: Attribution{Consumer: "console", Actor: "oidc-person"}}
	in := toolregistry.Server{ID: toolID("mcp", 0), Tenant: tenant, Key: "obs", Name: "Observability", URL: "https://mcp.example.test/mcp",
		Transport: toolregistry.TransportStreamableHTTP, Auth: toolregistry.Auth{Kind: toolregistry.AuthBearer},
		Credential: secretstore.Ref{Store: "openbao", Path: "mcp-servers/" + toolID("mcp", 0), Version: 1},
		Tools:      []toolregistry.Tool{{Name: "search_logs", Access: toolregistry.AccessRead}}}
	out, created, err := testStore.CreateMCPServer(ctx, in, 2, audit)
	if err != nil || !created || out.Revision == 0 {
		t.Fatalf("%v %v %+v", err, created, out)
	}
	again := in
	again.ID = toolID("mcp", 1)
	same, created, err := testStore.CreateMCPServer(ctx, again, 2, audit)
	if err != nil || created || same.ID != out.ID {
		t.Fatalf("a repeated create is not idempotent: %v %v %+v", err, created, same)
	}
	again.Name = "Other"
	var exists *toolregistry.ExistsError
	if _, _, err := testStore.CreateMCPServer(ctx, again, 2, audit); !errors.As(err, &exists) {
		t.Fatalf("got %v, want ExistsError", err)
	}
	other := in
	other.ID, other.Key = toolID("mcp", 2), "obs-two"
	if _, _, err := testStore.CreateMCPServer(ctx, other, 2, audit); err != nil {
		t.Fatal(err)
	}
	other.ID, other.Key = toolID("mcp", 3), "obs-three"
	if _, _, err := testStore.CreateMCPServer(ctx, other, 2, audit); !errors.Is(err, toolregistry.ErrLimit) {
		t.Fatalf("got %v, want ErrLimit", err)
	}
	next := out
	next.Name, next.Credential = "Renamed", secretstore.Ref{}
	var stale *toolregistry.StaleError
	if _, _, err := testStore.ReplaceMCPServer(ctx, next, out.Revision-1, audit); !errors.As(err, &stale) {
		t.Fatalf("got %v, want StaleError", err)
	}
	_, replaced, err := testStore.ReplaceMCPServer(ctx, next, out.Revision, audit)
	if err != nil || replaced.Name != "Renamed" || replaced.Credential != in.Credential || replaced.Revision == out.Revision {
		t.Fatalf("%v %+v", err, replaced)
	}
	var rows int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action LIKE 'mcp_server.%' AND detail->'server'->>'id' = $1`, out.ID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("audit rows: %d %v", rows, err)
	}
}

func TestToolRegistryRefusesACredentialValueInTheReferenceColumn(t *testing.T) {
	_, err := testStore.pool.Exec(context.Background(), `INSERT INTO mcp_servers (id, server_key, name, url, transport, auth, credential, tools, created_by, updated_by)
		VALUES ($1, 'leak', 'leak', 'https://x.example.test/mcp', 'streamable-http', '{}', '{"store":"s","path":"p","value":"secret"}', '[]', 'a', 'a')`, toolID("mcp", 9))
	if err == nil {
		t.Fatal("a credential value was stored in the reference column")
	}
}

func TestTeamToolsRefuseRemovalOfAnAttachedServerAndCreateOnlyOnce(t *testing.T) {
	ctx := context.Background()
	tenant := toolTenant(t)
	audit := ToolAudit{Attribution: Attribution{Actor: "oidc-person"}}
	srv, _, err := testStore.CreateMCPServer(ctx, toolregistry.Server{ID: toolID("mcp", 4), Tenant: tenant, Key: "obs", Name: "obs", URL: "https://mcp.example.test/mcp",
		Transport: toolregistry.TransportStreamableHTTP, Auth: toolregistry.Auth{Kind: toolregistry.AuthNone},
		Tools: []toolregistry.Tool{{Name: "search_logs", Access: toolregistry.AccessRead}}}, 10, audit)
	if err != nil {
		t.Fatal(err)
	}
	team := toolregistry.Team{Key: "bronze"}
	roles := map[string]toolregistry.RoleTools{"builder": {MCP: []toolregistry.ServerUse{{Server: srv.ID, Tools: []string{"search_logs"}}}}}
	a, err := testStore.ReplaceTeamTools(ctx, tenant, team, roles, nil, audit)
	if err != nil || a.Revision == 0 || a.TeamID != "" {
		t.Fatalf("%v %+v", err, a)
	}
	var stale *toolregistry.StaleError
	if _, err := testStore.ReplaceTeamTools(ctx, tenant, team, roles, nil, audit); !errors.As(err, &stale) {
		t.Fatalf("a second create: got %v, want StaleError", err)
	}
	var inUse *toolregistry.InUseError
	if _, err := testStore.RemoveMCPServer(ctx, tenant, srv.ID, audit); !errors.As(err, &inUse) || len(inUse.Teams) != 1 || inUse.Teams[0] != "bronze" {
		t.Fatalf("got %v, want InUseError naming bronze", err)
	}
	withID := toolregistry.Team{ID: "team_01jd8x4k2v6r9t3m5n7p0q2s4w", Key: "bronze"}
	found, err := testStore.TeamTools(ctx, tenant, withID)
	if err != nil || found.Revision != a.Revision {
		t.Fatalf("a key-stored attachment is not found once the Team has an id: %v", err)
	}
	if _, err := testStore.ReplaceTeamTools(ctx, tenant, withID, map[string]toolregistry.RoleTools{"*": {}}, &a.Revision, audit); err != nil {
		t.Fatal(err)
	}
	if err := testStore.RemoveTeamTools(ctx, tenant, withID, audit); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.RemoveMCPServer(ctx, tenant, srv.ID, audit); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.TeamTools(ctx, tenant, team); !errors.Is(err, toolregistry.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestSkillBundlesAreContentAddressed(t *testing.T) {
	ctx := context.Background()
	digest := "sha256:" + strings.Repeat("d", 64)
	if err := testStore.PutSkillBundle(ctx, digest, []byte("one"), []string{"review"}); err != nil {
		t.Fatal(err)
	}
	if err := testStore.PutSkillBundle(ctx, digest, []byte("two"), []string{"review"}); err != nil {
		t.Fatal(err)
	}
	got, err := testStore.SkillBundle(ctx, digest)
	if err != nil || string(got) != "one" {
		t.Fatalf("%q %v", got, err)
	}
	tenant := toolTenant(t)
	src, created, err := testStore.CreateSkillSource(ctx, toolregistry.SkillSource{ID: toolID("skl", 0), Tenant: tenant, Key: "review", Name: "review",
		Kind: toolregistry.SourceHTTPS, URL: "https://cdn.example.test/r.tgz", Digest: digest, BundleDigest: digest, Skills: []string{"review"}}, 10, ToolAudit{})
	if err != nil || !created || src.BundleDigest != digest {
		t.Fatalf("%v %+v", err, src)
	}
}
