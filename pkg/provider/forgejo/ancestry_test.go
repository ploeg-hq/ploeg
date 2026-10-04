package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAncestor_ComparesTheDeployedCommitWithTheMergeCommit(t *testing.T) {
	responses := map[string]string{
		"/api/v1/repos/webgrip/app/compare/d3...m1": `{"total_commits":0,"commits":[]}`,
		"/api/v1/repos/webgrip/app/compare/d1...m3": `{"total_commits":2,"commits":[{"sha":"m3"},{"sha":"m2"}]}`,
		"/api/v1/repos/webgrip/app/compare/d3...x9": `{"total_commits":1,"commits":[{"sha":"x9"}]}`,
	}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()
	for _, tc := range []struct {
		name, ancestor, descendant string
		want                       bool
	}{
		{"merged before the deploy", "m1", "d3", true},
		{"merged after the deploy", "m3", "d1", false},
		{"on a branch the deploy never had", "x9", "d3", false},
	} {
		got, err := p.IsAncestor(ctx, "webgrip/app", tc.ancestor, tc.descendant)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	if auth != "token tok" {
		t.Errorf("authorization = %q", auth)
	}
	if _, err := p.IsAncestor(ctx, "webgrip/app", "unknown", "d3"); err == nil {
		t.Error("a commit the forge does not know must be an error, not a no")
	}
	if _, err := p.IsAncestor(ctx, "app", "m1", "d3"); err == nil {
		t.Error("a repository without an owner must be refused")
	}
}
