package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

// runtimeOnlyGitLab is the GitLab forge of a release that configures no
// static GitLab: it acts on each project through the connection its runtime
// target was granted through, as ploegd wires it.
func runtimeOnlyGitLab(rig *targetRig, gl *projectGitLab) *gitlab.Provider {
	return &gitlab.Provider{BaseURL: gl.url, HookSecret: rig.conns.WebhookSecret,
		TokenFor: func(ctx context.Context, project string) (string, error) {
			return rig.svc.ActingToken(ctx, "gitlab-main", project)
		}}
}

func TestARuntimeOnlyGitLabReleaseAdmitsClaimsReceivesAndPostsThroughTheConnection(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	gl := rig.withGitLab(t)
	rig.register(t, `{"connection":"gl-team","repository":"acme/team/app","key":"team-app"}`)
	ctx := context.Background()

	if v := rig.server.Readiness.Admit(ctx, "team-app"); !v.Ready {
		t.Fatalf("the runtime target is not admitted: %+v", v)
	}

	reset(t)
	pinned := &work.Target{Forge: "gitlab-main", Owner: "acme/team", Repo: "app", BaseBranch: "main"}
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "7101", Team: "bronze", Title: "t", Target: pinned})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-7101", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}

	forge := runtimeOnlyGitLab(rig, gl)
	rig.server.LeaseTTL = time.Minute
	rig.server.WorkerSecurity = &WorkerSecurity{AllowLegacy: true}
	rig.server.Forges = map[string]provider.ForgeProvider{"gitlab-main": forge, "gitlab": forge}
	rig.server.ClaimForges = &ForgeDirectory{Default: "forgejo", Forges: map[string]ClaimForge{},
		Runtime: map[string]ClaimForge{"gitlab-main": {Dialect: "gitlab", URL: gl.url, PloegdURL: gl.url}}}
	rig.server.ForgeCreds = &forgebroker.WithConnections{Resolve: func(_ context.Context, connection string) (forgebroker.Broker, error) {
		if connection != "gl-team" {
			return nil, errors.New("minted through " + connection)
		}
		return &forgebroker.GitLab{APIURL: gl.url + "/api/v4", Token: glServiceToken}, nil
	}}
	handler := rig.server.Handler()

	code, claimed := postClaim(t, handler, `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
	if code != http.StatusOK {
		t.Fatalf("claim = %d; a Run on a runtime GitLab target is claimed without a static forge", code)
	}
	if claimed.ForgeDialect != "gitlab" || claimed.ForgeURL != gl.url || claimed.ForgeToken != "run-pat-77" {
		t.Fatalf("claim forge = %q %q token %q; want the runtime instance and a token minted through the connection",
			claimed.ForgeDialect, claimed.ForgeURL, claimed.ForgeToken)
	}
	gl.mu.Lock()
	minted := len(gl.mints)
	gl.mu.Unlock()
	if minted != 1 {
		t.Fatalf("mints = %d, want one project access token for the Run", minted)
	}

	delivery := `{"object_kind":"merge_request","user":{"username":"maintainer"},"project":{"path_with_namespace":"acme/team/app"},` +
		`"object_attributes":{"iid":7,"action":"merge","source_branch":"agent/vik-7101","last_commit":{"id":"abc123"},"merge_commit_sha":"def456"}}`
	r := httptest.NewRequest(http.MethodPost, "/webhooks/forge/gitlab?connection=gl-team", strings.NewReader(delivery))
	r.Header.Set("X-Gitlab-Token", glHookKey)
	r.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	r.Header.Set("X-Gitlab-Event-UUID", "runtime-only-merge-1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("merge request delivery = %d %s", w.Code, w.Body.String())
	}
	if n := rig.rows(t, `SELECT count(*) FROM audit_log WHERE actor = 'webhook:gitlab' AND action = 'forge.pr_merged' AND detail->>'repo' = 'acme/team/app'`); n != 1 {
		t.Fatalf("merge events recorded = %d; the runtime project's delivery is acted on, not only acknowledged", n)
	}

	forged := httptest.NewRequest(http.MethodPost, "/webhooks/forge/gitlab?connection=gl-team", strings.NewReader(delivery))
	forged.Header.Set("X-Gitlab-Token", "not-the-connections-secret")
	forged.Header.Set("X-Gitlab-Event-UUID", "runtime-only-merge-2")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, forged)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a delivery without the connection's secret = %d, want 400", w.Code)
	}

	if err := rig.server.Forges[pinned.Forge].Comment(ctx, "acme/team/app", 7, "Ploeg's run card"); err != nil {
		t.Fatalf("the card was not posted through the connection's credential: %v", err)
	}
	gl.mu.Lock()
	notes := len(gl.notes)
	gl.mu.Unlock()
	if notes != 1 {
		t.Fatalf("notes = %d, want the card", notes)
	}
	if err := forge.Comment(ctx, "acme/elsewhere", 7, "card"); !errors.Is(err, worktarget.ErrNoActingConnection) {
		t.Fatalf("a project no runtime target registers = %v, want ErrNoActingConnection", err)
	}
}
