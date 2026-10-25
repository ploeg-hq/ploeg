package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/signingkeys"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/txntoken"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

const (
	txnIssuer  = "https://ploeg.example"
	txnDomain  = "urn:ploeg:example"
	txnSponsor = "oidc-00000000000000000000000000sponsor"
)

func txnSigner(t *testing.T) *txntoken.Signer {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := txntoken.NewSigner(txnIssuer, txnDomain, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// txnWorkerFixture is a managed ploegd that signs transaction tokens, with
// the bronze Team sponsored and one reviewer Run pending.
func txnWorkerFixture(t *testing.T) (*Server, string) {
	t.Helper()
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	s.TransactionTokens = txnSigner(t)
	s.TeamSponsors = map[string]string{"bronze": txnSponsor}
	return s, bootstrap
}

type txnClaim struct {
	RunToken         string `json:"runToken"`
	ControlToken     string `json:"controlToken"`
	TransactionToken string `json:"transactionToken"`
}

func claimTxn(t *testing.T, s *Server, bootstrap, worker string) txnClaim {
	t.Helper()
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, worker, `{"team":"bronze","role":"reviewer"}`)
	var c txnClaim
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&c) != nil || c.TransactionToken == "" {
		t.Fatalf("claim %d %s", w.Code, w.Body.String())
	}
	return c
}

// publishedTxnKeys reads the key set the way a consumer does: unauthenticated
// from the well-known path.
func publishedTxnKeys(t *testing.T, s *Server) map[string]*ecdsa.PublicKey {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", TransactionTokenKeysPath, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/jwk-set+json" {
		t.Fatalf("key set %d %v", w.Code, w.Header())
	}
	keys, err := signingkeys.ParseKeySet(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func verifyTxn(t *testing.T, s *Server, token string) txntoken.Claims {
	t.Helper()
	c, err := txntoken.Verify(token, publishedTxnKeys(t, s), txntoken.Expect{Issuer: txnIssuer, Audience: txnDomain})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAClaimedRunGetsATransactionTokenThatActsForTheTeamsSponsor(t *testing.T) {
	s, bootstrap := txnWorkerFixture(t)
	c := claimTxn(t, s, bootstrap, "pod")
	if c.ControlToken == "" {
		t.Fatal("the HMAC capability is still issued by default for one release")
	}
	claims := verifyTxn(t, s, c.TransactionToken)
	run, err := testStore.RunControl(context.Background(), c.RunToken)
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(run.ID, 10)
	if claims.Subject != txnSponsor || claims.TCtx.SubjectProof != txntoken.ProofSponsor || claims.Txn != id ||
		claims.Act.Sub != "bronze/reviewer" || claims.Act.Run != id || claims.Act.Act != nil || claims.ReqWL != "pod" ||
		claims.Scope != txntoken.ScopeRead || claims.TCtx.Team != "bronze" || claims.TCtx.Role != "reviewer" ||
		claims.TCtx.WorkItem == "" || claims.TCtx.Shift == "" || claims.TCtx.Branch != "agent/vik-auth-fixture" {
		t.Fatalf("claims %+v", claims)
	}
	if got := claims.ExpiresAt.Time; got.Sub(run.Deadline.Truncate(time.Second)).Abs() > time.Second {
		t.Fatalf("exp %v, want the Run's deadline %v", got, run.Deadline)
	}
	if strings.Contains(c.TransactionToken, c.RunToken) {
		t.Fatal("the transaction token carries the Run token")
	}
}

func TestAHandedOverPersonIsTheSubjectAndTheIdentityProvidersActorIsNested(t *testing.T) {
	s, bootstrap := txnWorkerFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET acts_for='oidc-person', acts_for_proof='proven', acts_for_act='authentik-actor-bronze'`); err != nil {
		t.Fatal(err)
	}
	claims := verifyTxn(t, s, claimTxn(t, s, bootstrap, "pod").TransactionToken)
	if claims.Subject != "oidc-person" || claims.TCtx.SubjectProof != txntoken.ProofProven ||
		claims.Act.Sub != "bronze/reviewer" || claims.Act.Act == nil || claims.Act.Act.Sub != "authentik-actor-bronze" {
		t.Fatalf("claims %+v act %+v", claims, claims.Act)
	}
}

func TestAnAssertedPersonIsMarkedAsserted(t *testing.T) {
	s, bootstrap := txnWorkerFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET acts_for='oidc-mapped', acts_for_proof='', acts_for_act='ignored'`); err != nil {
		t.Fatal(err)
	}
	claims := verifyTxn(t, s, claimTxn(t, s, bootstrap, "pod").TransactionToken)
	if claims.Subject != "oidc-mapped" || claims.TCtx.SubjectProof != txntoken.ProofAsserted || claims.Act.Act != nil {
		t.Fatalf("claims %+v", claims)
	}
}

func TestATeamWithoutASponsorRunsNothing(t *testing.T) {
	s, bootstrap := txnWorkerFixture(t)
	s.TeamSponsors = nil
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `Team "bronze" has no sponsor`) || !strings.Contains(w.Body.String(), "teams.bronze.sponsor") {
		t.Fatalf("claim %d %s", w.Code, w.Body.String())
	}
	var running int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_runs WHERE state='running'`).Scan(&running); err != nil || running != 0 {
		t.Fatalf("running %d %v", running, err)
	}
	s.TransactionTokens = nil
	if got := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`).Code; got != 200 {
		t.Fatalf("without transaction tokens a Team needs no sponsor yet: %d", got)
	}
}

func TestTheTransactionTokenBindsItsRunWorkerAndDeadline(t *testing.T) {
	s, bootstrap := txnWorkerFixture(t)
	c := claimTxn(t, s, bootstrap, "pod")
	path := "/api/v1/runs/" + c.RunToken + "/renew"
	if got := workerRequest(s, "POST", path, c.TransactionToken, "other-pod", "").Code; got != 401 {
		t.Fatalf("another worker=%d", got)
	}
	foreign := txnSigner(t)
	forged, _, err := foreign.Sign(txntoken.Run{ID: verifyTxn(t, s, c.TransactionToken).Txn, Person: "oidc-x", Proof: txntoken.ProofProven,
		Team: "bronze", Role: "reviewer", Worker: "pod", Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if got := workerRequest(s, "POST", path, forged, "pod", "").Code; got != 401 {
		t.Fatalf("a token of another key=%d", got)
	}
	otherRun, _, _ := s.TransactionTokens.Sign(txntoken.Run{ID: "999999", Person: "oidc-x", Proof: txntoken.ProofProven,
		Team: "bronze", Role: "reviewer", Worker: "pod", Deadline: time.Now().Add(time.Minute)})
	if got := workerRequest(s, "POST", path, otherRun, "pod", "").Code; got != 401 {
		t.Fatalf("a token of another Run=%d", got)
	}
	w := workerRequest(s, "POST", path, c.TransactionToken, "pod", "")
	var renewed struct {
		TransactionToken string    `json:"transactionToken"`
		Deadline         time.Time `json:"deadline"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&renewed) != nil || renewed.TransactionToken == "" {
		t.Fatalf("renew %d %s", w.Code, w.Body.String())
	}
	if got := verifyTxn(t, s, renewed.TransactionToken).ExpiresAt.Time; got.Sub(renewed.Deadline.Truncate(time.Second)).Abs() > time.Second {
		t.Fatalf("renewed exp %v, want the renewed deadline %v", got, renewed.Deadline)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET expires_at=now()-interval '1 second' WHERE run_token=$1`, c.RunToken); err != nil {
		t.Fatal(err)
	}
	if got := workerRequest(s, "POST", path, renewed.TransactionToken, "pod", "").Code; got != 401 {
		t.Fatalf("a lapsed Run=%d", got)
	}
}

func TestRetiringTheHMACCapabilityIssuesAndAcceptsOnlyTheTransactionToken(t *testing.T) {
	s, bootstrap := txnWorkerFixture(t)
	hmac := s.WorkerSecurity
	retired, err := NewTransactionTokenWorkerSecurity(`[{"token":"` + bootstrap + `","team":"bronze","role":"reviewer"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s.WorkerSecurity = retired
	c := claimTxn(t, s, bootstrap, "pod")
	if c.ControlToken != "" {
		t.Fatal("a retired HMAC capability was issued")
	}
	capability := hmac.sign(workerCapability{Audience: "ploeg-worker-run", RunToken: c.RunToken, WorkerID: "pod", Team: "bronze", Role: "reviewer", Expires: time.Now().Add(time.Hour).Unix()})
	path := "/api/v1/runs/" + c.RunToken + "/renew"
	if got := workerRequest(s, "POST", path, capability, "pod", "").Code; got != 401 {
		t.Fatalf("a retired HMAC capability=%d", got)
	}
	if got := workerRequest(s, "POST", path, c.TransactionToken, "pod", "").Code; got != 200 {
		t.Fatalf("the transaction token=%d", got)
	}
	s.WorkerSecurity = hmac
	if got := workerRequest(s, "POST", path, capability, "pod", "").Code; got != 200 {
		t.Fatalf("by default the HMAC capability is still accepted=%d", got)
	}
}

func TestTheKeySetIsAbsentWithoutASigner(t *testing.T) {
	s, _ := secureWorkerFixture(t, &managedBrokerFixture{})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", TransactionTokenKeysPath, nil))
	if w.Code != 404 {
		t.Fatalf("key set without a signer: %d", w.Code)
	}
}

type recordingTransport struct {
	mu   sync.Mutex
	auth map[string]string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.auth[req.Method+" "+req.URL.Path] = strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	r.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

// A worker claims, renews, checkpoints and reports with ploeg-worker's own
// client, presenting the transaction token on every Run route and the renewed
// one after each renewal; the audit rows of its requests name the person and
// the agent from that token.
func TestAWorkerUsesItsTransactionTokenEndToEnd(t *testing.T) {
	s, bootstrap := txnWorkerFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET acts_for='oidc-person', acts_for_proof='proven', acts_for_act='authentik-actor-bronze'`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	rec := &recordingTransport{auth: map[string]string{}}
	api := &worker.APIClient{Base: srv.URL, HC: &http.Client{Transport: rec}, BootstrapToken: bootstrap, WorkerID: "pod-e2e"}
	claim, err := api.Claim("bronze", "reviewer")
	if err != nil || claim == nil || claim.TransactionToken == "" {
		t.Fatalf("claim %+v %v", claim, err)
	}
	renewal, err := api.Renew(context.Background(), claim.RunToken)
	if err != nil || renewal.Gone {
		t.Fatalf("renew %+v %v", renewal, err)
	}
	runPath := "/api/v1/runs/" + claim.RunToken
	if got := rec.auth["POST "+runPath+"/renew"]; got != claim.TransactionToken {
		t.Fatal("the worker renewed without its transaction token")
	}
	if err := api.Checkpoint(claim.RunToken, work.Checkpoint{Phase: "harness_started"}); err != nil {
		t.Fatal(err)
	}
	checkpointed := rec.auth["POST "+runPath+"/checkpoint"]
	if !txntoken.LooksSigned(checkpointed) || checkpointed == claim.TransactionToken {
		t.Fatal("the worker did not present the token its renewal answered")
	}
	if err := api.Outcome(claim.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "reviewed", Findings: "ok"}); err != nil {
		t.Fatal(err)
	}
	if !txntoken.LooksSigned(rec.auth["POST "+runPath+"/outcome"]) {
		t.Fatal("the outcome was not reported with the transaction token")
	}
	run, err := testStore.RunControl(context.Background(), claim.RunToken)
	if err != nil || run.State != "finished" {
		t.Fatalf("run %+v %v", run, err)
	}
	rows, err := testPool.Query(context.Background(), `SELECT action, detail->'runActor' FROM audit_log WHERE detail ? 'runActor' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action string
		var raw []byte
		if err := rows.Scan(&action, &raw); err != nil {
			t.Fatal(err)
		}
		var actor store.RunActor
		if err := json.Unmarshal(raw, &actor); err != nil {
			t.Fatal(err)
		}
		want := store.RunActor{Person: "oidc-person", Proof: txntoken.ProofProven, Agent: "bronze/reviewer", Act: "authentik-actor-bronze", Run: strconv.FormatInt(run.ID, 10)}
		if actor != want {
			t.Fatalf("%s runActor %+v, want %+v", action, actor, want)
		}
		actions = append(actions, action)
	}
	if len(actions) == 0 {
		t.Fatal("no audit row of the worker's requests names its person and agent")
	}
	t.Logf("audit rows naming the Run's person: %v", actions)
}
