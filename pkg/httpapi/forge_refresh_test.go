package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func refreshForgeCredential(t *testing.T, h http.Handler, runToken string) (int, forgeCredentialResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runToken+"/forge-credential", nil))
	var resp forgeCredentialResponse
	if rec.Code == http.StatusOK {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, resp
}

func TestRefreshForgeCredential_MintsForTheRunsOwnRepositoryAndRevokesTheOldOne(t *testing.T) {
	reset(t)
	broker := &countingBroker{}
	h := claudeServer(broker, fakeSessionVerifier{})
	run := claimWriter(t, h, "1301")
	if got := leaseTokenID(t, run); got != "tok-1" {
		t.Fatalf("claim recorded %q, want tok-1", got)
	}
	code, resp := refreshForgeCredential(t, h, run)
	if code != http.StatusOK {
		t.Fatalf("refresh returned %d, want 200", code)
	}
	if resp.ForgeToken != "secret-tok-2-webgrip/fixture" {
		t.Fatalf("refreshed token = %q, want one minted for the Run's own repository", resp.ForgeToken)
	}
	if got := leaseTokenID(t, run); got != "tok-2" {
		t.Fatalf("lease records %q, want tok-2", got)
	}
	if len(broker.revoked) != 1 || broker.revoked[0] != "tok-1" {
		t.Fatalf("revoked %v, want [tok-1]", broker.revoked)
	}
}

func TestRefreshForgeCredential_RefusesARunItDoesNotKnow(t *testing.T) {
	reset(t)
	broker := &countingBroker{}
	h := claudeServer(broker, fakeSessionVerifier{})
	if code, _ := refreshForgeCredential(t, h, "no-such-run"); code != http.StatusNotFound {
		t.Fatalf("refresh for an unknown run returned %d, want 404", code)
	}
	if broker.minted != 0 {
		t.Fatalf("minted %d credentials for an unknown run", broker.minted)
	}
}

func TestForgeCredentialBodiesMatchTheRunAPIContract(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/run-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for def, body := range map[string]any{
		"forgeCredentialResponse": forgeCredentialResponse{ForgeToken: "ghs_x", ForgeTokenExpiresAt: &expires},
	} {
		schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/" + def)
		if err != nil {
			t.Fatalf("%s: %v", def, err)
		}
		b, _ := json.Marshal(body)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s violates the contract: %v", def, err)
		}
	}
}
