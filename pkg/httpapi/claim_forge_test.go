package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type askedBroker struct {
	mu      sync.Mutex
	asked   []forgebroker.MintRequest
	revoked []string
}

func (b *askedBroker) Mint(_ context.Context, req forgebroker.MintRequest) (forgebroker.Credential, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, req)
	id := "tok-" + req.RunToken[:6]
	if req.Read {
		id = "read-" + req.RunToken[:6]
	}
	return forgebroker.Credential{ID: id, Token: "secret-" + id, ExpiresAt: time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)}, nil
}

func (b *askedBroker) Revoke(_ context.Context, cred forgebroker.Credential) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revoked = append(b.revoked, cred.ID)
	return nil
}

func twoForges() *ForgeDirectory {
	return &ForgeDirectory{Default: "forgejo", Forges: map[string]ClaimForge{
		"forgejo": {Dialect: harness.ForgeForgejo, URL: "http://forgejo-http.forgejo.svc:3000"},
		"github":  {Dialect: harness.ForgeGitHub, URL: "https://github.com"},
	}}
}

func multiForgeServer(creds forgebroker.Broker) http.Handler {
	return (&Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		ForgeCreds:     creds,
		ClaimForges:    twoForges(),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
	}).Handler()
}

func shiftOn(t *testing.T, externalID, forge string, roles ...store.Role) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		Target: &work.Target{Forge: forge, Owner: "acme", Repo: "widgets"},
	})
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-"+externalID, 10)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, roles); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
}

func TestClaim_CarriesTheForgeResolvedFromTheWorkTarget(t *testing.T) {
	for forge, want := range map[string]ClaimForge{
		"github": {Dialect: "github", URL: "https://github.com"},
		"":       {Dialect: "forgejo", URL: "http://forgejo-http.forgejo.svc:3000"},
	} {
		t.Run("forge "+forge, func(t *testing.T) {
			reset(t)
			shiftOn(t, "1901", forge, store.Role{Name: "builder", Writes: true, Cap: 1})
			broker := &askedBroker{}
			code, resp := postClaim(t, multiForgeServer(broker), `{"team":"bronze","role":"builder"}`)
			if code != http.StatusOK {
				t.Fatalf("claim returned %d, want 200", code)
			}
			if resp.ForgeDialect != want.Dialect || resp.ForgeURL != want.URL {
				t.Errorf("claim forge = %q %q, want %+v", resp.ForgeDialect, resp.ForgeURL, want)
			}
			if len(broker.asked) != 1 || broker.asked[0].Forge != forge || broker.asked[0].Read {
				t.Errorf("mint asked %+v, want one push credential on forge %q", broker.asked, forge)
			}
		})
	}
}

func TestClaim_AReadingRunReceivesAMintedReadOnlyCredentialRevokedWhenItSettles(t *testing.T) {
	reset(t)
	shiftOn(t, "1902", "github", store.Role{Name: "reviewer", Writes: false, Cap: 1})
	broker := &askedBroker{}
	h := multiForgeServer(broker)
	code, resp := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200", code)
	}
	if len(broker.asked) != 1 || !broker.asked[0].Read || broker.asked[0].Forge != "github" ||
		broker.asked[0].Owner != "acme" || broker.asked[0].Repo != "widgets" {
		t.Fatalf("mint asked %+v, want one read credential for acme/widgets on github", broker.asked)
	}
	if !strings.HasPrefix(resp.ForgeToken, "secret-read-") || !resp.ForgeTokenPerRun || resp.ForgeTokenExpiresAt == nil {
		t.Fatalf("reader's claim carried %q per-run %v expires %v", resp.ForgeToken, resp.ForgeTokenPerRun, resp.ForgeTokenExpiresAt)
	}
	if resp.ForgeDialect != "github" || resp.ForgeURL != "https://github.com" {
		t.Errorf("reader's claim forge = %q %q", resp.ForgeDialect, resp.ForgeURL)
	}
	var recorded string
	if err := testPool.QueryRow(context.Background(), `SELECT forge_token_id FROM agent_runs WHERE run_token = $1`, resp.RunToken).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recorded, "read-") {
		t.Fatalf("the reader's credential is recorded as %q", recorded)
	}
	leased, err := testStore.LeasedRunTokens(context.Background())
	if err != nil || len(leased) != 1 || leased[0] != resp.RunToken {
		t.Fatalf("the forge sweep spares %v, %v; want the running reader", leased, err)
	}
	if rec := postOutcome(t, h, resp.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "looked", Verdict: "approve"}); rec.Code != http.StatusNoContent {
		t.Fatalf("outcome returned %d: %s", rec.Code, rec.Body.String())
	}
	if len(broker.revoked) != 1 || broker.revoked[0] != recorded {
		t.Fatalf("revoked %v, want the reader's credential %s", broker.revoked, recorded)
	}
	if leased, _ := testStore.LeasedRunTokens(context.Background()); len(leased) != 0 {
		t.Fatalf("the forge sweep still spares %v after the reader settled", leased)
	}
}

func TestClaim_AReadingRunGetsNoCredentialFromABrokerThatMintsNone(t *testing.T) {
	reset(t)
	shiftOn(t, "1903", "", store.Role{Name: "reviewer", Writes: false, Cap: 1})
	code, resp := postClaim(t, forgeClaimServer(forgebroker.Static{Token: "the-shared-write-token"}), `{"team":"bronze","role":"reviewer"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200", code)
	}
	if resp.ForgeToken != "" || resp.ForgeTokenPerRun {
		t.Fatalf("a reader received %q per-run %v; it keeps its pod's read-only token", resp.ForgeToken, resp.ForgeTokenPerRun)
	}
}

func TestClaim_RefusesAWorkTargetOnAForgeTheReleaseDoesNotServe(t *testing.T) {
	reset(t)
	shiftOn(t, "1904", "gitlab", store.Role{Name: "builder", Writes: true, Cap: 1})
	broker := &askedBroker{}
	code, _ := postClaim(t, multiForgeServer(broker), `{"team":"bronze","role":"builder"}`)
	if code != http.StatusNoContent {
		t.Fatalf("claim returned %d, want 204", code)
	}
	if len(broker.asked) != 0 {
		t.Fatalf("minted %+v for a forge the release does not serve", broker.asked)
	}
	var outcome, reason string
	if err := testPool.QueryRow(context.Background(),
		`SELECT outcome, stuck_reason FROM agent_runs WHERE state = 'finished'`).Scan(&outcome, &reason); err != nil {
		t.Fatalf("read the refused Run: %v", err)
	}
	if outcome != string(work.OutcomeStuck) || !strings.Contains(reason, `forge "gitlab" is not configured`) {
		t.Errorf("refused Run = %s %q, want stuck naming the forge", outcome, reason)
	}
}

func TestRefreshForgeCredential_AReadingRunGetsAFreshReadCredential(t *testing.T) {
	reset(t)
	shiftOn(t, "1905", "github", store.Role{Name: "reviewer", Writes: false, Cap: 1})
	broker := &askedBroker{}
	h := multiForgeServer(broker)
	_, resp := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	code, refreshed := refreshForgeCredential(t, h, resp.RunToken)
	if code != http.StatusOK || refreshed.ForgeToken == "" {
		t.Fatalf("refresh returned %d %+v", code, refreshed)
	}
	if len(broker.asked) != 2 || !broker.asked[1].Read || broker.asked[1].Forge != "github" {
		t.Fatalf("mints asked %+v, want a second read credential on github", broker.asked)
	}
}
