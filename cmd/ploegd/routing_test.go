package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/target"
)

func TestRoutingWithoutTargetsHasNoGateAndIgnoresLabels(t *testing.T) {
	t.Setenv("PLOEG_TARGET_MAP", "10=webgrip/glide@development")
	resolver, gate, err := routing(context.Background(), slog.New(slog.DiscardHandler), &config.File{}, nil, nil, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if gate != nil {
		t.Error("a deployment without targets must not gate anything")
	}
	route, ok, err := resolver.Route(target.Request{Scope: "10", Labels: []string{"repo/homelab-cluster"}})
	if err != nil || !ok || route.Target.Repo != "glide" || route.Hint != "" {
		t.Errorf("route = %+v ok %v err %v, want the env rule unchanged", route, ok, err)
	}
}

func TestRoutingGatesTheRegisteredTargets(t *testing.T) {
	cfg := &config.File{
		Targets:  map[string]config.Target{"glide": {Repo: "webgrip/glide", Branch: "development"}},
		Trackers: config.Trackers{Vikunja: config.TrackerConfig{Projects: []config.Project{{ID: "10", Default: "glide"}}}},
	}
	resolver, gate, err := routing(context.Background(), slog.New(slog.DiscardHandler), cfg, nil, map[string]provider.ForgeProvider{}, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if gate == nil || resolver.Registered()["glide"].Forge != "forgejo" {
		t.Fatalf("gate %v registered %+v", gate, resolver.Registered())
	}
	if v := gate.Admit(context.Background(), "glide"); v.Ready {
		t.Error("a target on a forge that cannot report readiness must not be ready")
	}
}

func TestRoutingAdmitsAHealthyRegisteredGitLabTarget(t *testing.T) {
	const project = "/api/v4/projects/acme%2Finternal%2Fwidgets"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case project:
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case project + "/repository/files/AGENTS.md":
			if r.URL.Query().Get("ref") != "development" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := &config.File{
		Targets:  map[string]config.Target{"widgets": {Repo: "acme/internal/widgets", Branch: "development", Forge: "gitlab"}},
		Trackers: config.Trackers{Vikunja: config.TrackerConfig{Projects: []config.Project{{ID: "10", Default: "widgets"}}}},
	}
	forges := map[string]provider.ForgeProvider{"gitlab": &gitlab.Provider{BaseURL: srv.URL, Token: "tok"}}
	_, gate, err := routing(context.Background(), slog.New(slog.DiscardHandler), cfg, nil, forges, "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if gate == nil {
		t.Fatal("registered targets must be gated")
	}
	if v := gate.Admit(context.Background(), "widgets"); !v.Ready {
		t.Errorf("verdict = %+v, want a healthy GitLab target admitted", v)
	}
}
