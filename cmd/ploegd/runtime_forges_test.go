package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

func runtimeGitLabConnections() *connection.Service {
	return &connection.Service{
		Config: &connection.Config{Allow: []connection.Allow{
			{Port: "forge", Kind: "gitlab", Instance: "gitlab-main", Methods: []string{"gitlab_service_account"}},
			{Port: "tracker", Kind: "clickup", Instance: "clickup", Methods: []string{"oauth"}},
		}},
		Instances: map[string]provider.ConnectionInstance{
			"gitlab-main": {ID: "gitlab-main", Kind: "gitlab", URL: "https://gitlab.example/"},
			"clickup":     {ID: "clickup", Kind: "clickup", URL: "https://api.clickup.com"},
		},
	}
}

func TestARuntimeOnlyGitLabIsTheForgeOfItsInstanceAndActsThroughItsTargets(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	forges := map[string]provider.ForgeProvider{}
	runtime := runtimeForges(log, runtimeGitLabConnections(), forges, &worktarget.Service{})
	if got := runtime["gitlab-main"]; got.Dialect != "gitlab" || got.URL != "https://gitlab.example" || len(runtime) != 1 {
		t.Fatalf("runtime forges = %+v; want the GitLab instance alone, at its URL", runtime)
	}
	gl, ok := forges["gitlab-main"].(*gitlab.Provider)
	if !ok || forges["gitlab"] != forges["gitlab-main"] || gl.Token != "" || gl.Secret != "" || gl.TokenFor == nil || gl.BaseURL != "https://gitlab.example" {
		t.Fatalf("forges = %+v; want one GitLab provider without a static credential under the instance id and the webhook route", forges)
	}
	if _, err := gl.TokenFor(context.Background(), "acme/app"); !errors.Is(err, worktarget.ErrNoActingConnection) {
		t.Fatalf("a project no runtime target registers = %v, want ErrNoActingConnection", err)
	}

	release := releaseForges{registry: forges, defaultForge: "forgejo", byDialect: map[string]activeForge{}, runtime: runtime}
	d := release.directory()
	if d == nil || len(d.Forges) != 0 {
		t.Fatalf("directory = %+v; the runtime provider is no static forge", d)
	}
	if f, ok := d.Resolve("gitlab-main"); !ok || f.URL != "https://gitlab.example" || f.Dialect != "gitlab" {
		t.Fatalf("gitlab-main resolves to %+v %v", f, ok)
	}
	if f, ok := d.Resolve(""); ok {
		t.Fatalf("a target naming no forge resolved to %+v; nothing falls back to a runtime instance", f)
	}
}

func TestAStaticForgeOfTheDialectKeepsServingItsRuntimeTargetsAsBefore(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	static := &gitlab.Provider{BaseURL: "https://gitlab.example", Token: "static"}
	forges := map[string]provider.ForgeProvider{"gitlab": static}
	if runtime := runtimeForges(log, runtimeGitLabConnections(), forges, &worktarget.Service{}); len(runtime) != 0 {
		t.Fatalf("runtime forges = %+v beside a static GitLab", runtime)
	}
	if len(forges) != 1 || forges["gitlab"] != static {
		t.Fatalf("forges = %+v; a static GitLab release is unchanged", forges)
	}
	if runtime := runtimeForges(log, nil, map[string]provider.ForgeProvider{}, nil); runtime != nil {
		t.Fatalf("runtime forges without connections = %+v", runtime)
	}
	before := releaseOf("gitlab", nil, nil, gitlabAt("https://gitlab.example")).directory()
	if before == nil || len(before.Runtime) != 0 || before.Forges["gitlab"].Dialect != "gitlab" {
		t.Fatalf("static directory = %+v", before)
	}
	if none := (releaseForges{registry: map[string]provider.ForgeProvider{}, byDialect: map[string]activeForge{}}).directory(); none != nil {
		t.Fatalf("a release without any forge resolves %+v", none)
	}
}

func TestRunStartsWithOnlyARuntimeGitLabConnection(t *testing.T) {
	dbURL := startEmbeddedPostgres(t)
	addr := freeAddr(t)
	cfg := filepath.Join(t.TempDir(), "ploeg.yaml")
	if err := os.WriteFile(cfg, []byte(`connections:
  enabled: true
  allow:
    - {port: forge, kind: gitlab, instance: gitlab-main, methods: [gitlab_service_account]}
  instances:
    gitlab-main:
      kind: gitlab
      url: https://gitlab.example.com
targetRegistration:
  enabled: true
  connections: ["*"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLOEG_LISTEN", addr)
	t.Setenv("PLOEG_DATABASE_URL", dbURL)
	t.Setenv("PLOEG_WORKER_AUTH_MODE", "legacy")
	t.Setenv("PLOEG_CONFIG", cfg)
	for _, unset := range []string{"PLOEG_WORKER_SIGNING_KEY", "PLOEG_WORKER_BOOTSTRAPS", "LITELLM_ADMIN_URL", "PLOEG_FORGEJO_URL", "PLOEG_GITLAB_URL",
		"PLOEG_GITLAB_TOKEN", "PLOEG_GITLAB_MINT_PER_RUN", "PLOEG_GITHUB_URL", "PLOEG_VIKUNJA_URL", "PLOEG_CLICKUP_SECRET", "PLOEG_CLICKUP_TOKEN",
		"PLOEG_FORGEJO_BOT_PASSWORD", "PLOEG_GITHUB_APP_ID", "PLOEG_GITHUB_APP_PRIVATE_KEY", "PLOEG_PUBLIC_WEBHOOK_BASE"} {
		t.Setenv(unset, "")
		_ = os.Unsetenv(unset)
	}
	d := startRun(t)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-d.stopped:
			t.Fatalf("ploegd stopped: %v", d.err)
		default:
		}
		if code, _, _, err := get(addr, "/readyz"); err == nil && code == http.StatusOK {
			resp, err := http.Post("http://"+addr+"/webhooks/forge/gitlab?connection=gl-none", "application/json", strings.NewReader(`{"object_kind":"merge_request"}`))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("a delivery for a connection Ploeg does not hold = %d, want 400", resp.StatusCode)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ploegd with only a runtime GitLab connection never became ready")
}
