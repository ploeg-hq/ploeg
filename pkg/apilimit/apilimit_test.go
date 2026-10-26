package apilimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func limitedServer(t *testing.T, retryAfter string, rejections int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= rejections {
			w.Header().Set("Retry-After", retryAfter)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func get(ctx context.Context, rt http.RoundTripper, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "fixture-token")
	return (&http.Client{Transport: rt}).Do(req)
}

func TestA429IsRetriedAfterItsRetryAfter(t *testing.T) {
	srv, calls := limitedServer(t, "1", 1)
	start := time.Now()
	resp, err := get(context.Background(), New("clickup", Policies["clickup"], http.DefaultTransport), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("status %d after %d calls, want 200 after 2", resp.StatusCode, calls.Load())
	}
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("retried after %s, before the asked second", waited)
	}
}

func TestADeadlineShorterThanRetryAfterReturnsPromptly(t *testing.T) {
	srv, calls := limitedServer(t, "3", 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	resp, err := get(ctx, New("clickup", Policies["clickup"], http.DefaultTransport), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("waited %s on a Retry-After the deadline cannot cover", waited)
	}
	if resp.StatusCode != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls, want the 429 after 1", resp.StatusCode, calls.Load())
	}
}

func TestARetryAfterLongerThanMaxWaitIsNotSleptOut(t *testing.T) {
	srv, calls := limitedServer(t, "120", 1)
	start := time.Now()
	resp, err := get(context.Background(), New("clickup", Policies["clickup"], http.DefaultTransport), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if time.Since(start) > 500*time.Millisecond || resp.StatusCode != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls in %s", resp.StatusCode, calls.Load(), time.Since(start))
	}
}

func TestTheBucketCapsTheRate(t *testing.T) {
	srv, calls := limitedServer(t, "", 0)
	rt := New("clickup", Policy{PerMinute: 6, Burst: 3, Retry: true}, http.DefaultTransport)
	refused := 0
	for range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		resp, err := get(ctx, rt, srv.URL)
		cancel()
		var throttled *ThrottledError
		switch {
		case errors.As(err, &throttled):
			refused++
		case err != nil:
			t.Fatal(err)
		default:
			resp.Body.Close()
		}
	}
	if calls.Load() != 3 || refused != 7 {
		t.Fatalf("upstream saw %d calls and %d were refused, want 3 and 7", calls.Load(), refused)
	}
}

func TestA503WithoutRetryAfterAndAPostAfter503AreNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == http.MethodPost {
			w.Header().Set("Retry-After", "0")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	rt := New("forgejo", Policies["forgejo"], http.DefaultTransport)
	resp, err := get(context.Background(), rt, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	resp, err = (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 2 {
		t.Fatalf("upstream saw %d calls, want 2", calls.Load())
	}
}

func TestCountsReachTheSnapshot(t *testing.T) {
	before := snapshotOf("vikunja")
	srv, _ := limitedServer(t, "0", 1)
	resp, err := get(context.Background(), New("vikunja", Policies["vikunja"], http.DefaultTransport), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	after := snapshotOf("vikunja")
	if after.Requests-before.Requests != 2 || after.RateLimited-before.RateLimited != 1 || after.Retries-before.Retries != 1 {
		t.Fatalf("counts moved from %+v to %+v", before, after)
	}
}

func snapshotOf(provider string) Counts {
	for _, c := range Snapshot() {
		if c.Provider == provider {
			return c
		}
	}
	return Counts{}
}
