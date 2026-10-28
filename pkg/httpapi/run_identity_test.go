package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/runidentity"
)

const (
	identityIssuer   = "https://ploeg.example"
	identityAudience = "https://observer.example/sessions"
)

func identitySigner(t *testing.T) *runidentity.Signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	s, err := runidentity.NewSigner(identityIssuer, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var identityAllowlist = runidentity.Audiences{
	"bronze": {
		"builder":  {identityAudience, "urn:example:second", "urn:example:a"},
		"reviewer": {"urn:example:a", "urn:example:reviewer-only"},
	},
}

func identityServer(signer *runidentity.Signer) *Server {
	return &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, RunIdentity: signer, RunIdentityAudiences: identityAllowlist}
}

func requestIdentity(t *testing.T, h http.Handler, runToken, body string) (*httptest.ResponseRecorder, runIdentityResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runToken+"/identity", strings.NewReader(body)))
	var resp runIdentityResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec, resp
}

func publishedKeys(t *testing.T, h http.Handler) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RunIdentityKeysPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("key set: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/jwk-set+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	return rec.Body.Bytes()
}

func TestRunIdentityNamesTheRunFromPloegdsOwnRecords(t *testing.T) {
	reset(t)
	signer := identitySigner(t)
	h := identityServer(signer).Handler()
	targetedWriterShift(t, "1490")
	code, claim := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"claude-code"}`)
	if code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	rec, resp := requestIdentity(t, h, claim.RunToken, `{"audiences":["`+identityAudience+`","urn:example:second"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("identity: %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if len(resp.Identities) != 2 || resp.Identities[0].Audience != identityAudience {
		t.Fatalf("identities = %+v", resp.Identities)
	}
	keys, err := runidentity.ParseKeySet(publishedKeys(t, h))
	if err != nil {
		t.Fatal(err)
	}
	got, err := runidentity.Verify(resp.Identities[0].Token, keys, runidentity.Expect{Issuer: identityIssuer, Audience: identityAudience})
	if err != nil {
		t.Fatalf("the identity does not verify against the published keys: %v", err)
	}
	var runID, workItemID, shiftID int64
	var round int
	var deadline time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT id, work_item_id, shift_id, round, expires_at FROM agent_runs WHERE run_token = $1`, claim.RunToken).
		Scan(&runID, &workItemID, &shiftID, &round, &deadline); err != nil {
		t.Fatal(err)
	}
	want := runidentity.Run{ID: strconv.FormatInt(runID, 10), WorkItemID: strconv.FormatInt(workItemID, 10), ShiftID: strconv.FormatInt(shiftID, 10),
		Round: round, Role: "builder", Team: "bronze", Harness: "claude-code", Branch: "agent/vik-1490", Trace: "ploeg-" + claim.RunToken[:12]}
	if got.Run != want {
		t.Errorf("run = %+v, want %+v", got.Run, want)
	}
	if exp := got.ExpiresAt.Time; exp.After(deadline) || deadline.Sub(exp) > time.Second {
		t.Errorf("expires %s, want the lease deadline %s", exp, deadline)
	}
	if _, err := runidentity.Verify(resp.Identities[1].Token, keys, runidentity.Expect{Issuer: identityIssuer, Audience: identityAudience}); err == nil {
		t.Error("the second audience's identity verified for the first audience")
	}
	if strings.Contains(resp.Identities[0].Token, claim.RunToken) {
		t.Error("the identity carries the Run token")
	}
}

func TestRunIdentityRefusals(t *testing.T) {
	reset(t)
	h := identityServer(identitySigner(t)).Handler()
	targetedWriterShift(t, "1491")
	_, claim := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"claude-code"}`)
	for name, body := range map[string]string{
		"no audience":      `{"audiences":[]}`,
		"plain http":       `{"audiences":["http://observer.example"]}`,
		"twice":            `{"audiences":["urn:example:a","urn:example:a"]}`,
		"too many":         `{"audiences":["urn:e:1","urn:e:2","urn:e:3","urn:e:4","urn:e:5","urn:e:6","urn:e:7","urn:e:8","urn:e:9"]}`,
		"not json":         `audiences`,
		"query on the url": `{"audiences":["https://observer.example/?k=v"]}`,
	} {
		if rec, _ := requestIdentity(t, h, claim.RunToken, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec, _ := requestIdentity(t, h, "no-such-run", `{"audiences":["urn:example:a"]}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d", rec.Code)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE run_token = $1`, claim.RunToken); err != nil {
		t.Fatal(err)
	}
	if rec, _ := requestIdentity(t, h, claim.RunToken, `{"audiences":["urn:example:a"]}`); rec.Code != http.StatusConflict {
		t.Errorf("lapsed lease: %d", rec.Code)
	}
	big := `{"audiences":["` + strings.Repeat("a", 5000) + `"]}`
	if rec, _ := requestIdentity(t, h, claim.RunToken, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}
}

func TestRunIdentityRefusesAnAudienceTheRunsTeamAndRoleDoNotList(t *testing.T) {
	reset(t)
	var logged bytes.Buffer
	s := identityServer(identitySigner(t))
	s.Log = slog.New(slog.NewJSONHandler(&logged, nil))
	h := s.Handler()
	targetedWriterShift(t, "1493")
	_, claim := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"claude-code"}`)
	for name, body := range map[string]string{
		"another service":         `{"audiences":["https://elsewhere.example/sessions"]}`,
		"another Role's audience": `{"audiences":["urn:example:reviewer-only"]}`,
		"one listed and one not":  `{"audiences":["` + identityAudience + `","https://elsewhere.example/sessions"]}`,
	} {
		logged.Reset()
		rec, _ := requestIdentity(t, h, claim.RunToken, body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "eyJ") {
			t.Errorf("%s: a refused request carried a token", name)
		}
		if !strings.Contains(logged.String(), `"msg":"run identity refused`) || !strings.Contains(logged.String(), `"team":"bronze"`) ||
			!strings.Contains(logged.String(), `"role":"builder"`) {
			t.Errorf("%s: the refusal was not recorded: %s", name, logged.String())
		}
	}
	s.RunIdentityAudiences = runidentity.Audiences{"bronze": {"reviewer": {identityAudience}}}
	if rec, _ := requestIdentity(t, h, claim.RunToken, `{"audiences":["`+identityAudience+`"]}`); rec.Code != http.StatusForbidden {
		t.Errorf("an audience listed only for another Role of the Team: %d, want 403", rec.Code)
	}
	s.RunIdentityAudiences = nil
	if rec, _ := requestIdentity(t, h, claim.RunToken, `{"audiences":["`+identityAudience+`"]}`); rec.Code != http.StatusForbidden {
		t.Errorf("no allowlist: %d, want 403", rec.Code)
	}
}

func TestRunIdentityIsOffWithoutASigningKey(t *testing.T) {
	reset(t)
	h := identityServer(nil).Handler()
	targetedWriterShift(t, "1492")
	_, claim := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if rec, _ := requestIdentity(t, h, claim.RunToken, `{"audiences":["urn:example:a"]}`); rec.Code != http.StatusNotFound {
		t.Errorf("identity without a key: %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RunIdentityKeysPath, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("key set without a key: %d", rec.Code)
	}
}

func TestManagedRunIdentityNeedsTheRunsControlCapability(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	s.RunIdentity = identitySigner(t)
	s.RunIdentityAudiences = identityAllowlist
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil {
		t.Fatalf("claim: %d", w.Code)
	}
	path := "/api/v1/runs/" + claim.RunToken + "/identity"
	body := `{"audiences":["urn:example:a"]}`
	for _, tc := range []struct{ token, worker string }{{"", "pod"}, {bootstrap, "pod"}, {claim.ControlToken, "other-pod"}} {
		if got := workerRequest(s, "POST", path, tc.token, tc.worker, body).Code; got != http.StatusUnauthorized {
			t.Errorf("identity with %q/%s = %d, want 401", tc.token, tc.worker, got)
		}
	}
	if got := workerRequest(s, "POST", path, claim.ControlToken, "pod", body).Code; got != http.StatusOK {
		t.Errorf("identity with the Run's capability = %d", got)
	}
	if got := workerRequest(s, "GET", RunIdentityKeysPath, "", "", "").Code; got != http.StatusOK {
		t.Errorf("the key set needs no credential, got %d", got)
	}
}

func TestRunIdentityBodiesMatchTheRunAPIContract(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/run-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	signer := identitySigner(t)
	token, exp, err := signer.Sign(runidentity.Run{ID: "1", WorkItemID: "2", ShiftID: "3", Round: 1, Role: "builder", Team: "bronze",
		Harness: "claude-code", Branch: "agent/vik-2", Trace: "ploeg-0123456789ab"}, identityAudience, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := runidentity.ParseKeySet(mustJSON(t, signer.KeySet()))
	claims, err := runidentity.Verify(token, keys, runidentity.Expect{Issuer: identityIssuer, Audience: identityAudience})
	if err != nil {
		t.Fatal(err)
	}
	for def, body := range map[string]any{
		"runIdentityRequest":  runIdentityRequest{Audiences: []string{identityAudience}},
		"runIdentityResponse": runIdentityResponse{Identities: []runIdentity{{Audience: identityAudience, Token: token, ExpiresAt: exp}}},
		"runIdentityClaims":   claims,
		"runIdentityKeySet":   signer.KeySet(),
	} {
		schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/" + def)
		if err != nil {
			t.Fatalf("%s: %v", def, err)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(mustJSON(t, body)))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s violates the run API schema: %v", def, err)
		}
	}
}
