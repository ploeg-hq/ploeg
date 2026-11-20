package clickup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestProbeConnectionSendsTheRawTokenClickUpAccepts(t *testing.T) {
	bodies := map[string]string{
		"/api/v2/user":            `{"user":{"id":42,"username":"Ploeg Bot"}}`,
		"/api/v2/team":            `{"teams":[{"id":"7"}]}`,
		"/api/v2/team/7/space":    `{"spaces":[{"id":"s1"}]}`,
		"/api/v2/space/s1/folder": `{"folders":[{"lists":[{"id":"l1","name":"Run"}]}]}`,
		"/api/v2/space/s1/list":   `{"lists":[]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pk_probe" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"err":"Token invalid","ECODE":"OAUTH_025"}`)
			return
		}
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	probe, err := Connector{}.ProbeConnection(context.Background(),
		provider.ConnectionInstance{Kind: "clickup", APIURL: srv.URL + "/api"},
		provider.ConnectionCredential{Port: "tracker", Method: "token", Token: "pk_probe"})
	if err != nil {
		t.Fatalf("probe with a personal token ClickUp accepts bare: %v", err)
	}
	if probe.Account.ID != "42" || probe.Account.Login != "Ploeg Bot" {
		t.Fatalf("account = %+v; want the token's own member", probe.Account)
	}
	if probe.Visible == nil || probe.Visible.Count != 1 || probe.Visible.First[0].ID != "l1" {
		t.Fatalf("visible = %+v; want the one List the token sees", probe.Visible)
	}
}
