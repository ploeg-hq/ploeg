package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
)

func forgejoAt(url string) activeForge {
	return activeForge{provider: &forgejo.Provider{BaseURL: url}, url: url}
}

func githubAt(url string) activeForge {
	return activeForge{provider: &github.Provider{BaseURL: url}, url: url}
}

func gitlabAt(url string) activeForge {
	return activeForge{provider: &gitlab.Provider{BaseURL: url}, url: url}
}

func releaseOf(defaultForge string, forgejoCreds forgebroker.Broker, app *githubapp.Minter, forges ...activeForge) releaseForges {
	r := releaseForges{registry: map[string]provider.ForgeProvider{}, defaultForge: defaultForge, byDialect: map[string]activeForge{},
		forgejoCreds: forgejoCreds, githubApp: app}
	for _, f := range forges {
		r.registry[f.provider.Name()] = f.provider
		r.byDialect[f.provider.Name()] = f
	}
	if fj, ok := forgejoCreds.(*forgebroker.Forgejo); ok {
		r.forgejoSweeper = fj
	}
	return r
}

func testGitHubApp(t *testing.T) *githubapp.Minter {
	t.Helper()
	_, app, err := githubForge(slog.New(slog.DiscardHandler), githubForgeEnv{url: "https://github.com", appID: "1", appKey: forgeAppKeyPEM(t)})
	if err != nil || app == nil {
		t.Fatalf("app %v err %v", app, err)
	}
	return app
}

func TestTwoForgesRefuseToStartWhenOneCannotMint(t *testing.T) {
	minting := &forgebroker.Forgejo{BaseURL: "https://forgejo.example", Bot: "agent-builder", Password: "pw"}
	cases := map[string]struct {
		release releaseForges
		names   []string
	}{
		"forgejo without its bot password": {
			releaseOf("forgejo", forgebroker.Static{Token: "shared"}, testGitHubApp(t), forgejoAt("https://forgejo.example"), githubAt("https://github.com")),
			[]string{"forge forgejo", "PLOEG_FORGEJO_BOT_PASSWORD"},
		},
		"github without an App": {
			releaseOf("forgejo", minting, nil, forgejoAt("https://forgejo.example"), githubAt("https://github.com")),
			[]string{"forge github", "PLOEG_GITHUB_APP_ID"},
		},
		"gitlab beside another forge": {
			releaseOf("forgejo", minting, nil, forgejoAt("https://forgejo.example"), gitlabAt("https://gitlab.example")),
			[]string{"forge gitlab", "PLOEG_GITLAB_MINT_PER_RUN"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.release.requireMinting()
			if err == nil {
				t.Fatal("the release started")
			}
			for _, want := range append(c.names, "ADR-0192") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not name %q", err, want)
				}
			}
		})
	}
}

func TestTwoMintingForgesStartAndRouteEachForgeToItsOwnBroker(t *testing.T) {
	minting := &forgebroker.Forgejo{BaseURL: "https://forgejo.example", Bot: "agent-builder", Password: "pw"}
	gh := githubAt("https://github.com")
	gh.workerURL = "https://github.com"
	fj := forgejoAt("https://forgejo.example")
	fj.workerURL = "http://forgejo-http.forgejo.svc:3000"
	release := releaseOf("forgejo", minting, testGitHubApp(t), fj, gh)
	if err := release.requireMinting(); err != nil {
		t.Fatalf("a release whose forges all mint was refused: %v", err)
	}
	broker, sweeper := release.credentials(slog.New(slog.DiscardHandler))
	router, ok := broker.(*forgebroker.Router)
	if !ok || sweeper == nil {
		t.Fatalf("broker %T sweeper %v, want a router", broker, sweeper)
	}
	if router.Forgejo != minting || strings.Join(router.ForgejoForges, ",") != "forgejo" ||
		router.GitHub == nil || strings.Join(router.GitHubForges, ",") != "github" {
		t.Fatalf("router = %+v", router)
	}
	d := release.directory()
	if f, ok := d.Resolve(""); !ok || f.Dialect != "forgejo" || f.URL != "http://forgejo-http.forgejo.svc:3000" {
		t.Errorf("the default forge resolves to %+v %v", f, ok)
	}
	if f, ok := d.Resolve("github"); !ok || f.Dialect != "github" || f.URL != "https://github.com" {
		t.Errorf("github resolves to %+v %v", f, ok)
	}
	if f, _ := d.Resolve(""); f.CommitName != "" || f.CommitEmail != "" {
		t.Errorf("a forge without a configured commit identity resolves to %q %q", f.CommitName, f.CommitEmail)
	}
	if _, ok := d.Resolve("gitlab"); ok {
		t.Error("a forge the release does not serve resolves")
	}
}

func TestOneForgeWithoutMintingStartsWithTheSharedTokenAsBefore(t *testing.T) {
	shared := forgebroker.Static{Token: "shared"}
	release := releaseOf("forgejo", shared, nil, forgejoAt("https://forgejo.example"))
	if err := release.requireMinting(); err != nil {
		t.Fatalf("a single forge without minting was refused: %v", err)
	}
	broker, sweeper := release.credentials(slog.New(slog.DiscardHandler))
	if broker != shared || sweeper != nil {
		t.Fatalf("broker %#v sweeper %v, want the shared token unchanged", broker, sweeper)
	}
	if f, ok := release.directory().Resolve(""); !ok || f.Dialect != "forgejo" || f.URL != "" {
		t.Fatalf("the single forge resolves to %+v %v; its workers keep FORGE_URL", f, ok)
	}
	gitlabOnly := releaseOf("gitlab", forgebroker.Static{}, nil, gitlabAt("https://gitlab.example"))
	if err := gitlabOnly.requireMinting(); err != nil {
		t.Fatalf("gitlab as the only forge was refused: %v", err)
	}
}

func TestRunRefusesToStartWithTwoForgesWhenOneCannotMint(t *testing.T) {
	dbURL := startEmbeddedPostgres(t)
	addr := freeAddr(t)
	t.Setenv("PLOEG_LISTEN", addr)
	t.Setenv("PLOEG_DATABASE_URL", dbURL)
	t.Setenv("PLOEG_WORKER_AUTH_MODE", "legacy")
	for _, unset := range []string{"PLOEG_WORKER_SIGNING_KEY", "PLOEG_WORKER_BOOTSTRAPS", "PLOEG_CONFIG", "LITELLM_ADMIN_URL", "PLOEG_GITLAB_URL",
		"PLOEG_VIKUNJA_URL", "PLOEG_CLICKUP_SECRET", "PLOEG_CLICKUP_TOKEN", "PLOEG_FORGEJO_BOT_PASSWORD", "PLOEG_GITHUB_APP_ID", "PLOEG_GITHUB_APP_PRIVATE_KEY"} {
		t.Setenv(unset, "")
		_ = os.Unsetenv(unset)
	}
	t.Setenv("PLOEG_FORGEJO_URL", forgeReportingVersion(t, "15.0.1"))
	t.Setenv("PLOEG_FORGEJO_TOKEN", "shared")
	t.Setenv("PLOEG_GITHUB_URL", "https://github.com")
	t.Setenv("PLOEG_GITHUB_TOKEN", "github_pat_x")
	d := startRun(t)

	select {
	case <-d.stopped:
		if d.err == nil || !strings.Contains(d.err.Error(), "forge forgejo cannot mint") || !strings.Contains(d.err.Error(), "forge github cannot mint") {
			t.Fatalf("run returned %v, want a refusal naming both forges", d.err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("ploegd kept running with two forges that cannot mint")
	}
	if code, _, _, err := get(addr, "/readyz"); err == nil && code == http.StatusOK {
		t.Fatal("ploegd answered ready")
	}
}

func TestRunStartsWithOneForgeAndTheSharedToken(t *testing.T) {
	dbURL := startEmbeddedPostgres(t)
	addr := freeAddr(t)
	t.Setenv("PLOEG_LISTEN", addr)
	t.Setenv("PLOEG_DATABASE_URL", dbURL)
	t.Setenv("PLOEG_WORKER_AUTH_MODE", "legacy")
	for _, unset := range []string{"PLOEG_WORKER_SIGNING_KEY", "PLOEG_WORKER_BOOTSTRAPS", "PLOEG_CONFIG", "LITELLM_ADMIN_URL", "PLOEG_GITLAB_URL", "PLOEG_GITHUB_URL",
		"PLOEG_VIKUNJA_URL", "PLOEG_CLICKUP_SECRET", "PLOEG_CLICKUP_TOKEN", "PLOEG_FORGEJO_BOT_PASSWORD"} {
		t.Setenv(unset, "")
		_ = os.Unsetenv(unset)
	}
	t.Setenv("PLOEG_FORGEJO_URL", "https://forgejo.example")
	t.Setenv("PLOEG_FORGEJO_TOKEN", "shared")
	d := startRun(t)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-d.stopped:
			t.Fatalf("ploegd stopped: %v", d.err)
		default:
		}
		if code, _, _, err := get(addr, "/readyz"); err == nil && code == http.StatusOK {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ploegd with one forge and the shared token never became ready")
}

func TestEachForgeCarriesTheCommitIdentityItsEnvironmentConfigures(t *testing.T) {
	t.Setenv("PLOEG_FORGEJO_COMMIT_NAME", "forgejo-agent")
	t.Setenv("PLOEG_FORGEJO_COMMIT_EMAIL", " forgejo-agent@forgejo.example ")
	t.Setenv("PLOEG_GITHUB_COMMIT_NAME", "ploeg[bot]")
	t.Setenv("PLOEG_GITHUB_COMMIT_EMAIL", "1+ploeg[bot]@users.noreply.github.com")
	fj := activeForgeFromEnv(&forgejo.Provider{BaseURL: "https://forgejo.example"}, "https://forgejo.example")
	gh := activeForgeFromEnv(&github.Provider{BaseURL: "https://github.com"}, "https://github.com")
	gl := activeForgeFromEnv(&gitlab.Provider{BaseURL: "https://gitlab.example"}, "https://gitlab.example")
	d := releaseOf("forgejo", forgebroker.Static{}, nil, fj, gh, gl).directory()
	for id, want := range map[string][2]string{
		"forgejo": {"forgejo-agent", "forgejo-agent@forgejo.example"},
		"github":  {"ploeg[bot]", "1+ploeg[bot]@users.noreply.github.com"},
		"gitlab":  {"", ""},
	} {
		f, ok := d.Resolve(id)
		if !ok || f.CommitName != want[0] || f.CommitEmail != want[1] {
			t.Errorf("%s resolves to %+v %v, want commit identity %q", id, f, ok, want)
		}
	}
}
