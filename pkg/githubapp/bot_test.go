package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBotIsTheAppsBotUserWithItsNoReplyAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("GET /app without the App JWT")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"slug": "ploeg-fixture"})
		case "/users/ploeg-fixture[bot]":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "ploeg-fixture[bot]", "id": 41898282})
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	bot, err := testMinter(t, srv.URL).Bot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bot.Login != "ploeg-fixture[bot]" || bot.NoReplyEmail() != "41898282+ploeg-fixture[bot]@users.noreply.github.com" {
		t.Fatalf("bot = %+v %s", bot, bot.NoReplyEmail())
	}
}
