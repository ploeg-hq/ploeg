package worker

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ADR-0091: the worker names its harness and agent profile in every claim,
// so ploegd can record them on the Run.
func TestClaimNamesTheHarnessAndItsProfile(t *testing.T) {
	for _, tc := range []struct {
		hc      HarnessConfig
		harness string
		profile string
	}{
		{HarnessConfig{Name: "acp"}, "acp", "opencode"},
		{HarnessConfig{Name: "acp", ACP: ACPConfig{Profile: "qwen-code"}}, "acp", "qwen-code"},
		{HarnessConfig{Name: "openhands"}, "openhands", ""},
	} {
		adapter, err := NewAdapter(tc.hc)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			rw.WriteHeader(http.StatusNoContent)
		}))
		w := New(Config{APIURL: srv.URL}, adapter, nil, slog.New(slog.DiscardHandler))
		if _, err := w.API.Claim("bronze", "builder"); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		_, hasProfile := got["harnessProfile"]
		if got["harness"] != tc.harness || got["harnessProfile"] != tc.profile || hasProfile != (tc.profile != "") {
			t.Fatalf("%+v: claim body = %v, want harness %q profile %q", tc.hc, got, tc.harness, tc.profile)
		}
	}
}
