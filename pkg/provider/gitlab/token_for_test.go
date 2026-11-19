package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenForActsOnEachProjectWithItsOwnTokenAndFailsClosed(t *testing.T) {
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.EscapedPath()] = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	refused := errors.New("no connection reaches the project")
	p := &Provider{BaseURL: srv.URL, Token: "static-token", TokenFor: func(_ context.Context, project string) (string, error) {
		switch project {
		case "acme/team/app":
			return "token-team", nil
		case "acme/other":
			return "token-other", nil
		}
		return "", refused
	}}
	ctx := context.Background()
	if err := p.Comment(ctx, "acme/team/app", 7, "card"); err != nil {
		t.Fatal(err)
	}
	if err := p.Comment(ctx, "acme/other", 3, "card"); err != nil {
		t.Fatal(err)
	}
	if got := seen["/api/v4/projects/acme%2Fteam%2Fapp/merge_requests/7/notes"]; got != "token-team" {
		t.Fatalf("acme/team/app was sent %q, want its own connection's token", got)
	}
	if got := seen["/api/v4/projects/acme%2Fother/merge_requests/3/notes"]; got != "token-other" {
		t.Fatalf("acme/other was sent %q, want its own connection's token", got)
	}
	before := len(seen)
	if err := p.Comment(ctx, "acme/unknown", 1, "card"); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if _, err := p.PullRequestFacts(ctx, "acme/unknown", 1); !errors.Is(err, refused) {
		t.Fatalf("facts err = %v, want the refusal", err)
	}
	if len(seen) != before {
		t.Fatal("a request without a token reached GitLab")
	}
}
