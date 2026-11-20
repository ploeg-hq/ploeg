package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/forgeidentity"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
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
		"forgejo": {Dialect: harness.ForgeForgejo, URL: "http://forgejo-http.forgejo.svc:3000", PloegdURL: "https://forgejo.example.com",
			Committer: func(context.Context, string, string) (forgeidentity.Identity, error) {
				return forgeidentity.Identity{Name: "Ploeg Bot", Email: "bot@forgejo.example.com"}, nil
			}},
		"github": {Dialect: harness.ForgeGitHub, URL: "https://github.com", PloegdURL: "https://github.com"},
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
		"github": {Dialect: "github", URL: "https://github.com", PloegdURL: "https://github.com"},
		"":       {Dialect: "forgejo", URL: "http://forgejo-http.forgejo.svc:3000", PloegdURL: "https://forgejo.example.com"},
	} {
		t.Run("forge "+forge, func(t *testing.T) {
			reset(t)
			shiftOn(t, "1901", forge, store.Role{Name: "builder", Writes: true, Cap: 1})
			broker := &askedBroker{}
			code, resp := postClaim(t, multiForgeServer(broker), `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
			if code != http.StatusOK {
				t.Fatalf("claim returned %d, want 200", code)
			}
			if resp.ForgeDialect != want.Dialect || resp.ForgeURL != want.URL {
				t.Errorf("claim forge = %q %q, want %+v", resp.ForgeDialect, resp.ForgeURL, want)
			}
			if len(broker.asked) != 1 || broker.asked[0].Forge != forge || broker.asked[0].Read {
				t.Errorf("mint asked %+v, want one push credential on forge %q", broker.asked, forge)
			}
			a := resp.Attribution
			if a == nil || a.Run == "" {
				t.Fatalf("attribution = %+v, want the Run's id", a)
			}
			switch {
			case forge == "" && (a.Committer == nil || *a.Committer != work.GitIdentity{Name: "Ploeg Bot", Email: "bot@forgejo.example.com"}):
				t.Errorf("the forgejo Run commits as %+v, want the forge account", a.Committer)
			case forge == "github" && a.Committer != nil:
				t.Errorf("a forge without a committer source named %+v", a.Committer)
			}
		})
	}
}

func TestClaim_AReadingRunReceivesAMintedReadOnlyCredentialRevokedWhenItSettles(t *testing.T) {
	reset(t)
	shiftOn(t, "1902", "github", store.Role{Name: "reviewer", Writes: false, Cap: 1})
	broker := &askedBroker{}
	h := multiForgeServer(broker)
	code, resp := postClaim(t, h, `{"team":"bronze","role":"reviewer","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
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
	code, resp := postClaim(t, forgeClaimServer(forgebroker.Static{Token: "the-shared-write-token"}), `{"team":"bronze","role":"reviewer","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
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
	code, _ := postClaim(t, multiForgeServer(broker), `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
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

func TestClaim_AnUnknownForgeParksItsWorkItemForAPerson(t *testing.T) {
	reset(t)
	shiftOn(t, "1906", "gitlab", store.Role{Name: "builder", Writes: true, Cap: 1})
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler), ForgeCreds: &askedBroker{},
		ClaimForges: twoForges(), WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Engine: &shiftengine.Engine{Store: testStore, Plans: plan.Plans{}, Uniform: true, Log: slog.New(slog.DiscardHandler)}}
	if code, _ := postClaim(t, s.Handler(), `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`); code != http.StatusNoContent {
		t.Fatalf("claim returned %d, want 204", code)
	}
	var state string
	var closed bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT w.state, sh.closed_at IS NOT NULL FROM work_items w JOIN shifts sh ON sh.work_item_id = w.id WHERE w.external_id = '1906'`).Scan(&state, &closed); err != nil {
		t.Fatal(err)
	}
	if state != string(work.StateNeedsHuman) || !closed {
		t.Errorf("work item %s, shift closed %v; want needs_human with its Shift closed", state, closed)
	}
}

func TestClaim_ASingleForgeReleaseResolvesAnUnregisteredForgeToItsOnlyForge(t *testing.T) {
	reset(t)
	shiftOn(t, "1907", "forgejo-before-the-rename", store.Role{Name: "builder", Writes: true, Cap: 1})
	broker := &askedBroker{}
	h := (&Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler), ForgeCreds: broker,
		ClaimForges: &ForgeDirectory{Default: "forgejo", Forges: map[string]ClaimForge{
			"forgejo": {Dialect: harness.ForgeForgejo, PloegdURL: "https://forgejo.example.com"},
		}},
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}}).Handler()
	code, resp := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200: a single-forge release ignored the id before ADR-0192", code)
	}
	if resp.ForgeDialect != harness.ForgeForgejo || resp.WorkItem.Target == nil || resp.WorkItem.Target.Forge != "forgejo" {
		t.Errorf("claim forge %q, target %+v; want the only forge", resp.ForgeDialect, resp.WorkItem.Target)
	}
	if len(broker.asked) != 1 || broker.asked[0].Forge != "forgejo" {
		t.Errorf("mint asked %+v, want one credential for the only forge", broker.asked)
	}
}

func TestClaim_ALegacyClaimInAMultiForgeReleaseCarriesACredentialMintedForIt(t *testing.T) {
	reset(t)
	if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{
		Provider: "vikunja", ExternalID: "1908", Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "github", Owner: "acme", Repo: "widgets"},
	}); err != nil {
		t.Fatal(err)
	}
	broker := &askedBroker{}
	h := multiForgeServer(broker)
	code, resp := postClaim(t, h, `{"team":"bronze","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
	if code != http.StatusOK {
		t.Fatalf("legacy claim returned %d, want 200", code)
	}
	if resp.Shift != 0 {
		t.Fatalf("the claim took Shift %d; this test needs the shift-less path", resp.Shift)
	}
	if len(broker.asked) != 1 || broker.asked[0].Forge != "github" || broker.asked[0].Read {
		t.Fatalf("mint asked %+v, want one push credential on github", broker.asked)
	}
	if !strings.HasPrefix(resp.ForgeToken, "secret-tok-") || !resp.ForgeTokenPerRun || resp.ForgeURL != "https://github.com" {
		t.Fatalf("legacy claim carried token %q per-run %v forge %q", resp.ForgeToken, resp.ForgeTokenPerRun, resp.ForgeURL)
	}
	if rec := postOutcome(t, h, resp.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "nothing to do"}); rec.Code != http.StatusNoContent {
		t.Fatalf("outcome returned %d: %s", rec.Code, rec.Body.String())
	}
	if len(broker.revoked) != 1 || !strings.HasPrefix(broker.revoked[0], "tok-") {
		t.Fatalf("revoked %v, want the legacy Run's credential", broker.revoked)
	}
}

func TestClaudeForgeCredential_MintsForTheRunsForgeAndNamesItsRemote(t *testing.T) {
	reset(t)
	shiftOn(t, "1909", "github", store.Role{Name: "builder", Writes: true, Cap: 1})
	broker := &askedBroker{}
	h := (&Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler), ForgeCreds: broker,
		ClaimForges: twoForges(), WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		ClaudeSessions: &ClaudeSessions{Verifier: fakeSessionVerifier{claims: sessionClaims("gh")}, GitUser: "agent-builder", ForgeURL: "https://forgejo.example.com"},
	}).Handler()
	code, resp := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d", code)
	}
	if code := recordSession(t, h, resp.RunToken, testEnvironment, "session_gh"); code != http.StatusNoContent {
		t.Fatalf("record session returned %d", code)
	}
	code, cred := exchange(t, h)
	if code != http.StatusOK {
		t.Fatalf("exchange returned %d, want 200", code)
	}
	if last := broker.asked[len(broker.asked)-1]; last.Forge != "github" || last.Read {
		t.Errorf("the exchange minted %+v, want a push credential on github", last)
	}
	if cred.Remote != "https://github.com/acme/widgets.git" || cred.Username != "x-access-token" {
		t.Errorf("exchange remote %q user %q, want the GitHub repository as x-access-token", cred.Remote, cred.Username)
	}
}

func TestClaudeForgeCredential_RefusesAForgeWithoutAURLASessionCanPushTo(t *testing.T) {
	reset(t)
	shiftOn(t, "1910", "github", store.Role{Name: "builder", Writes: true, Cap: 1})
	broker := &askedBroker{}
	forges := twoForges()
	forges.Forges["github"] = ClaimForge{Dialect: harness.ForgeGitHub, URL: "https://github.com"}
	h := (&Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler), ForgeCreds: broker,
		ClaimForges: forges, WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		ClaudeSessions: &ClaudeSessions{Verifier: fakeSessionVerifier{claims: sessionClaims("nourl")}, GitUser: "agent-builder", ForgeURL: "https://forgejo.example.com"},
	}).Handler()
	_, resp := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
	recordSession(t, h, resp.RunToken, testEnvironment, "session_nourl")
	minted := len(broker.asked)
	if code, cred := exchange(t, h); code != http.StatusConflict || cred.Token != "" {
		t.Fatalf("exchange returned %d with a token %v, want 409 and none", code, cred.Token != "")
	}
	if len(broker.asked) != minted {
		t.Errorf("the refused exchange minted %+v", broker.asked[minted:])
	}
}

func TestClientClaim_DispatchesTheRunsForgeAndRefusesOneTheReleaseDoesNotServe(t *testing.T) {
	for _, tc := range []struct {
		forge     string
		wantCode  int
		wantInAPI string
	}{
		{forge: "github", wantCode: http.StatusOK, wantInAPI: "https://api.github.com/repos/webgrip/fixture/pulls"},
		{forge: "gitlab", wantCode: http.StatusConflict},
	} {
		t.Run(tc.forge, func(t *testing.T) {
			reset(t)
			s, token := clientCarriedServer(t)
			s.ClaimForges = twoForges()
			id := clientCarriedWork(t, s, "1911", "ryan", clientPerson)
			if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET target_forge = $2 WHERE id = $1`, id, tc.forge); err != nil {
				t.Fatal(err)
			}
			w := clientClaim(s, token, clientPerson, "", id)
			if w.Code != tc.wantCode {
				t.Fatalf("claim: %d %s, want %d", w.Code, w.Body, tc.wantCode)
			}
			if tc.wantCode != http.StatusOK {
				if code := operatorErrorCode(t, w); code != "forge_unavailable" {
					t.Errorf("refusal code %q, want forge_unavailable", code)
				}
				if states := runStates(t, id); states != "pending" {
					t.Errorf("runs %q after a refused claim, want the Run still pending", states)
				}
				return
			}
			var resp clientClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(resp.Dispatch.Prompt, tc.wantInAPI) || strings.Contains(resp.Dispatch.Prompt, "forge.example") {
				t.Errorf("the dispatch prompt does not send the Run to its forge's API %s", tc.wantInAPI)
			}
		})
	}
}

func TestRefreshForgeCredential_AReadingRunGetsAFreshReadCredential(t *testing.T) {
	reset(t)
	shiftOn(t, "1905", "github", store.Role{Name: "reviewer", Writes: false, Cap: 1})
	broker := &askedBroker{}
	h := multiForgeServer(broker)
	_, resp := postClaim(t, h, `{"team":"bronze","role":"reviewer","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
	code, refreshed := refreshForgeCredential(t, h, resp.RunToken)
	if code != http.StatusOK || refreshed.ForgeToken == "" {
		t.Fatalf("refresh returned %d %+v", code, refreshed)
	}
	if len(broker.asked) != 2 || !broker.asked[1].Read || broker.asked[1].Forge != "github" {
		t.Fatalf("mints asked %+v, want a second read credential on github", broker.asked)
	}
}

func TestClaim_AttributesTheRunToThePersonItActsFor(t *testing.T) {
	reset(t)
	shiftOn(t, "1905", "", store.Role{Name: "builder", Writes: true, Cap: 1})
	h := (&Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		ForgeCreds:     &askedBroker{},
		ClaimForges:    twoForges(),
		TeamSponsors:   map[string]string{"bronze": "oidc-0123456789abcdef0123456789abcdef"},
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
	}).Handler()
	code, resp := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200", code)
	}
	if a := resp.Attribution; a == nil || a.Person != "oidc-0123456789abcdef0123456789abcdef" || a.PersonProof != "sponsor" {
		t.Fatalf("attribution = %+v, want the Team's sponsor", a)
	}
}
