package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAncestor_ComparesThroughTheMergeBase(t *testing.T) {
	var gotPath, gotToken string
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken, gotQuery = r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN"), r.URL.Query()
		switch r.URL.Query().Get("to") {
		case "m1":
			_, _ = w.Write([]byte(`{"commit":null,"commits":[],"diffs":[],"compare_timeout":false,"compare_same_ref":false}`))
		case "m3":
			_, _ = w.Write([]byte(`{"commit":{"id":"m3"},"commits":[{"id":"m3"}],"diffs":[{"diff":"+x"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 Not Found"}`))
		}
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()
	if ok, err := p.IsAncestor(ctx, "group/sub/project", "m1", "d3"); !ok || err != nil {
		t.Errorf("ancestor: %v, %v", ok, err)
	}
	if gotPath != "/api/v4/projects/group%2Fsub%2Fproject/repository/compare" || gotToken != "tok" ||
		gotQuery["from"][0] != "d3" || gotQuery["straight"][0] != "false" {
		t.Errorf("request = %s %v (%s)", gotPath, gotQuery, gotToken)
	}
	if ok, err := p.IsAncestor(ctx, "group/sub/project", "m3", "d3"); ok || err != nil {
		t.Errorf("not an ancestor: %v, %v", ok, err)
	}
	if _, err := p.IsAncestor(ctx, "group/sub/project", "zz", "d3"); err == nil {
		t.Error("a 404 must be an error, not a no")
	}
}
