package llmbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
)

type probeGateway struct {
	listed     []litellm.KeyInfo
	listStatus int
	infoStatus int
}

func (p probeGateway) broker(t *testing.T) *LiteLLM {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key/list":
			if p.listStatus != 0 {
				w.WriteHeader(p.listStatus)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": p.listed, "total_count": len(p.listed), "total_pages": 1, "current_page": 1})
		case "/key/info":
			if r.URL.Query().Get("key") != "recorded-key-id" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(p.infoStatus)
			_, _ = w.Write([]byte(`{"info":{"spend":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return NewLiteLLM(litellm.NewClient(srv.URL, "test-master-key"))
}

func TestRunKeysGone(t *testing.T) {
	alias := litellm.Alias(runToken)
	cases := []struct {
		name    string
		gateway probeGateway
		keyIDs  []string
		gone    bool
		fails   bool
	}{
		{name: "no alias and recorded key not found", gateway: probeGateway{infoStatus: http.StatusNotFound}, keyIDs: []string{"recorded-key-id"}, gone: true},
		{name: "no alias and no recorded key", gateway: probeGateway{}, keyIDs: []string{""}, gone: true},
		{name: "alias still listed", gateway: probeGateway{listed: []litellm.KeyInfo{{Token: "other", KeyAlias: alias, Blocked: true}}, infoStatus: http.StatusNotFound}, keyIDs: []string{"recorded-key-id"}},
		{name: "recorded key still held", gateway: probeGateway{infoStatus: http.StatusOK}, keyIDs: []string{"recorded-key-id"}},
		{name: "key info fails", gateway: probeGateway{infoStatus: http.StatusInternalServerError}, keyIDs: []string{"recorded-key-id"}, fails: true},
		{name: "key list fails", gateway: probeGateway{listStatus: http.StatusBadGateway, infoStatus: http.StatusNotFound}, keyIDs: []string{"recorded-key-id"}, fails: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gone, err := tc.gateway.broker(t).RunKeysGone(context.Background(), runToken, tc.keyIDs)
			if (err != nil) != tc.fails || gone != tc.gone {
				t.Fatalf("gone=%v err=%v; want gone=%v fails=%v", gone, err, tc.gone, tc.fails)
			}
		})
	}
}
