package clickup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureTask = `{"id":"abc","status":{"status":"open","type":"custom"},"list":{"id":"901"}}`

func TestA429FromClickUpIsRetriedAfterItsReset(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Second).Unix()))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, fixtureTask)
	}))
	defer srv.Close()
	if _, err := (&Provider{BaseURL: srv.URL, Token: "pk_retry"}).BoardStatus(context.Background(), "abc"); err != nil {
		t.Fatalf("a 429 with a reset inside the wait budget failed the read: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("ClickUp saw %d calls, want 2", calls.Load())
	}
}

func TestClickUpReadsStayInsideThePerTokenLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, fixtureTask)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_bucket"}
	for range 40 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, _ = p.BoardStatus(ctx, "abc")
		cancel()
	}
	if got := calls.Load(); got > 21 {
		t.Fatalf("ClickUp saw %d calls in about a second from one token, want at most the burst of 20 and one refill", got)
	}
}
