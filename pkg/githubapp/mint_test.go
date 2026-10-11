package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func mintServer(t *testing.T, grant string, revoked *[]string) *httptest.Server {
	t.Helper()
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/widgets/installation":
			_, _ = io.WriteString(w, `{"id": 5}`)
		case "POST /app/installations/5/access_tokens":
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Repositories) != 1 || body.Repositories[0] != "widgets" || body.Permissions["contents"] != "write" || body.Permissions["workflows"] != "" {
				t.Errorf("token request = %+v", body)
			}
			n := mints.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"token":"ghs_`+string(rune('a'+n))+`","expires_at":"2027-01-15T09:00:00Z","permissions":`+grant+`}`)
		case "DELETE /installation/token":
			*revoked = append(*revoked, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testMinter(t *testing.T, apiURL string) *Minter {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &Minter{AppID: "1234", Key: key, APIURL: apiURL, Now: func() time.Time { return time.Unix(1_800_000_000, 0) }}
}

func TestMintGivesEachCallerItsOwnToken(t *testing.T) {
	var revoked []string
	m := testMinter(t, mintServer(t, `{"contents":"write","metadata":"read"}`, &revoked).URL)
	first, err := m.Mint(context.Background(), "acme", "widgets", WriterPermissions())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Mint(context.Background(), "acme", "widgets", WriterPermissions())
	if err != nil {
		t.Fatal(err)
	}
	if first.Value == second.Value || !first.ExpiresAt.Equal(time.Date(2027, 1, 15, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("tokens %+v and %+v: two Runs must not share one token", first, second)
	}
	if err := m.Revoke(context.Background(), first.Value); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != "Bearer "+first.Value {
		t.Fatalf("revoked %v", revoked)
	}
}

func TestMintRefusesAndRevokesAWiderGrant(t *testing.T) {
	var revoked []string
	m := testMinter(t, mintServer(t, `{"contents":"write","administration":"write"}`, &revoked).URL)
	if _, err := m.Mint(context.Background(), "acme", "widgets", WriterPermissions()); err == nil {
		t.Fatal("a token with administration:write was accepted")
	}
	if len(revoked) != 1 {
		t.Fatalf("the over-wide token was not revoked: %v", revoked)
	}
}

func TestRevokeTreatsAnEndedTokenAsRevoked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if err := (&Minter{APIURL: srv.URL}).Revoke(context.Background(), "ghs_gone"); err != nil {
		t.Fatal(err)
	}
}
