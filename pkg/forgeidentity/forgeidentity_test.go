package forgeidentity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func userServer(t *testing.T, path, header, value string, body map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Header.Get(header) != value {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func token(t string) TokenFor {
	return func(context.Context, string, string) (string, error) { return t, nil }
}

func TestFromTokenReadsTheAccountTheTokenBelongsTo(t *testing.T) {
	for name, tc := range map[string]struct {
		dialect, path, header, value string
		body                         map[string]any
		want                         Identity
	}{
		"forgejo": {"forgejo", "/api/v1/user", "Authorization", "token fj-token",
			map[string]any{"login": "bot", "full_name": "Ploeg Bot", "email": "bot@forge.example"},
			Identity{Name: "Ploeg Bot", Email: "bot@forge.example"}},
		"gitlab": {"gitlab", "/api/v4/user", "PRIVATE-TOKEN", "gl-token",
			map[string]any{"username": "group_123_bot", "name": "Ploeg", "email": "", "commit_email": "group_123_bot@noreply.gitlab.example"},
			Identity{Name: "Ploeg", Email: "group_123_bot@noreply.gitlab.example"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := userServer(t, tc.path, tc.header, tc.value, tc.body)
			got, err := FromToken(tc.dialect, srv.URL, token(map[string]string{"forgejo": "fj-token", "gitlab": "gl-token"}[tc.dialect]), nil)(context.Background(), "acme", "widgets")
			if err != nil || got != tc.want {
				t.Fatalf("identity = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestFromTokenOnGitHubIsTheAccountsNoReplyAddress(t *testing.T) {
	srv := userServer(t, "/api/v3/user", "Authorization", "Bearer gh-token", map[string]any{"login": "ploeg-bot", "id": 7, "name": ""})
	got, err := FromToken("github", srv.URL, token("gh-token"), nil)(context.Background(), "acme", "widgets")
	if err != nil || got != (Identity{Name: "ploeg-bot", Email: "7+ploeg-bot@users.noreply.github.com"}) {
		t.Fatalf("identity = %+v, %v", got, err)
	}
}

func TestFromTokenRefusesAnAccountWithoutAnEmail(t *testing.T) {
	srv := userServer(t, "/api/v1/user", "Authorization", "token t", map[string]any{"login": "bot", "email": ""})
	if _, err := FromToken("forgejo", srv.URL, token("t"), nil)(context.Background(), "", ""); err == nil {
		t.Fatal("an account without an e-mail gave an identity")
	}
}

func TestCachedReadsOnceAndRetriesAFailureLater(t *testing.T) {
	reads, fail := 0, true
	src := func(context.Context, string, string) (Identity, error) {
		reads++
		if fail {
			return Identity{}, errors.New("forge down")
		}
		return Identity{Name: "bot", Email: "bot@forge.example"}, nil
	}
	now := time.Unix(1_800_000_000, 0)
	c := &cache{src: src, key: Shared, entries: map[string]cacheEntry{}, now: func() time.Time { return now }}
	if _, err := c.get(context.Background(), "a", "b"); err == nil {
		t.Fatal("a failed read gave an identity")
	}
	_, _ = c.get(context.Background(), "a", "b")
	if reads != 1 {
		t.Fatalf("a failure was read again %d times before RetryAfter", reads)
	}
	fail, now = false, now.Add(RetryAfter)
	for range 3 {
		if id, err := c.get(context.Background(), "c", "d"); err != nil || id.Email != "bot@forge.example" {
			t.Fatalf("identity = %+v, %v", id, err)
		}
	}
	if reads != 2 {
		t.Fatalf("read %d times, want 2", reads)
	}
}
