package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestClaimDecodesTheForgeItCarries(t *testing.T) {
	body := `{"runToken":"` + strings.Repeat("a1", 24) + `","deadline":"2026-11-17T12:00:00Z","workItem":{"provider":"vikunja","externalId":"7","team":"bronze","title":"t"},
		"forgeToken":"ghs_x","forgeTokenPerRun":true,"forgeDialect":"github","forgeUrl":"https://github.com"}`
	var claimed ClaimResponse
	if err := json.Unmarshal([]byte(body), &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.ForgeDialect != harness.ForgeGitHub || claimed.ForgeURL != "https://github.com" {
		t.Fatalf("decoded forge %q %q", claimed.ForgeDialect, claimed.ForgeURL)
	}
}

func TestResolveClaimTarget_TheClaimsForgeWinsOverTheWorkersOwn(t *testing.T) {
	target := &work.Target{Forge: "github", Owner: "acme", Repo: "widgets", BaseBranch: "main"}
	cases := []struct {
		name     string
		cfg      Config
		claimed  ClaimResponse
		dialect  string
		url      string
		wantFail bool
	}{
		{name: "a multi-forge worker takes dialect and URL from the claim",
			cfg:     Config{},
			claimed: ClaimResponse{WorkItem: itemWith(target), ForgeDialect: "github", ForgeURL: "https://github.com"},
			dialect: "github", url: "https://github.com"},
		{name: "the claim's forge wins over a single-forge worker's env",
			cfg:     Config{ForgeURL: "http://forgejo", DefaultForge: "forgejo"},
			claimed: ClaimResponse{WorkItem: itemWith(target), ForgeDialect: "github", ForgeURL: "https://github.com"},
			dialect: "github", url: "https://github.com"},
		{name: "a claim without a URL keeps FORGE_URL",
			cfg:     Config{ForgeURL: "http://forgejo", DefaultForge: "forgejo"},
			claimed: ClaimResponse{WorkItem: itemWith(&work.Target{Owner: "acme", Repo: "widgets"}), ForgeDialect: "forgejo"},
			dialect: "forgejo", url: "http://forgejo"},
		{name: "an older ploegd's claim keeps the env forge",
			cfg:     Config{ForgeURL: "http://forgejo", DefaultForge: "forgejo"},
			claimed: ClaimResponse{WorkItem: itemWith(&work.Target{Owner: "acme", Repo: "widgets"})},
			dialect: "forgejo", url: "http://forgejo"},
		{name: "no forge URL anywhere is a configuration failure",
			cfg:      Config{},
			claimed:  ClaimResponse{WorkItem: itemWith(target), ForgeDialect: "github"},
			wantFail: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := resolveClaimTarget(c.cfg, &c.claimed, discardLog())
			if c.wantFail {
				if err == nil {
					t.Fatalf("resolved %+v", ref)
				}
				return
			}
			if err != nil || ref.Forge != c.dialect || ref.ForgeURL != c.url || ref.Owner != "acme" || ref.Name != "widgets" {
				t.Fatalf("ref = %+v, %v; want %s at %s", ref, err, c.dialect, c.url)
			}
		})
	}
}

type recordingForge struct {
	mu    sync.Mutex
	paths []string
	auth  []string
}

func (f *recordingForge) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAWorkerWithoutAnEnvForgeClonesFromTheClaimsForgeWithTheMintedCredential(t *testing.T) {
	forge := &recordingForge{}
	srv := forge.server(t)
	for _, c := range []struct {
		name   string
		writes bool
		role   string
	}{{"writer", true, "builder"}, {"reader", false, "reviewer"}} {
		t.Run(c.name, func(t *testing.T) {
			forge.mu.Lock()
			forge.paths, forge.auth = nil, nil
			forge.mu.Unlock()
			w := &Worker{Cfg: Config{WorkDir: t.TempDir()}, Log: discardLog()}
			claimed := &ClaimResponse{RunToken: "rt", Role: c.role, Writes: c.writes,
				WorkItem:     work.WorkItem{ID: "1", ExternalID: "7", Title: "t", Target: &work.Target{Forge: "github", Owner: "acme", Repo: "widgets", BaseBranch: "main"}},
				ForgeToken:   "minted-for-this-run",
				ForgeDialect: "github", ForgeURL: srv.URL, ForgeTokenPerRun: true}
			report := w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
			if report.Outcome != work.OutcomeStuck || report.Summary != "git clone failed" {
				t.Fatalf("outcome %q %q, want the clone against the fake forge to fail", report.Outcome, report.Summary)
			}
			forge.mu.Lock()
			defer forge.mu.Unlock()
			if len(forge.paths) == 0 || !strings.HasPrefix(forge.paths[0], "/acme/widgets.git/") {
				t.Fatalf("the clone asked %v, want the claim's forge for acme/widgets", forge.paths)
			}
			if !strings.Contains(strings.Join(forge.auth, " "), "Basic ") {
				t.Fatalf("the clone presented %v, want the minted credential", forge.auth)
			}
		})
	}
}

func TestAWorkerWithoutAnyForgeCredentialEndsTheRunStuck(t *testing.T) {
	w := &Worker{Cfg: Config{WorkDir: t.TempDir()}, Log: discardLog()}
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true,
		WorkItem:     work.WorkItem{ID: "1", ExternalID: "7", Title: "t", Target: &work.Target{Forge: "github", Owner: "acme", Repo: "widgets"}},
		ForgeDialect: "github", ForgeURL: "https://github.com"}
	report := w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
	if report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, "ADR-0192") {
		t.Fatalf("outcome %q %q, want stuck naming the missing credential", report.Outcome, report.StuckReason)
	}
}
