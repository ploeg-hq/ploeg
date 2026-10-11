package githubapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com":             "https://api.github.com",
		"https://GitHub.com/":            "https://api.github.com",
		"https://ghe.example.com":        "https://ghe.example.com/api/v3",
		"http://127.0.0.1:4567":          "http://127.0.0.1:4567/api/v3",
		"https://ghe.example.com/prefix": "https://ghe.example.com/prefix/api/v3",
	}
	for in, want := range cases {
		if got := APIURL(in); got != want {
			t.Errorf("APIURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNextLink(t *testing.T) {
	h := http.Header{}
	h.Set("Link", `<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next", <https://api.github.com/x?page=9>; rel="last"`)
	if got := NextLink(h); got != "https://api.github.com/x?page=3" {
		t.Fatalf("NextLink = %q", got)
	}
	h.Set("Link", `<https://api.github.com/x?page=1>; rel="first"`)
	if got := NextLink(h); got != "" {
		t.Fatalf("NextLink on the last page = %q", got)
	}
}

func recordingSleep(waits *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
}

func TestDoWaitsOutRateLimits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"a":1}` {
			t.Errorf("attempt %d body = %q", calls.Load()+1, body)
		}
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusForbidden)
		case 2:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(20*time.Second).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
		case 3:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	var waits []time.Duration
	c := &Client{Sleep: recordingSleep(&waits), Now: func() time.Time { return now }, Retries: 3, MaxWait: 2 * time.Minute}
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d after retries", resp.StatusCode)
	}
	want := []time.Duration{7 * time.Second, 21 * time.Second, 60 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
}

func TestDoReturnsALimitLongerThanMaxWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	var waits []time.Duration
	c := &Client{Sleep: recordingSleep(&waits)}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || len(waits) != 0 {
		t.Fatalf("status %d, waits %v: a wait beyond MaxWait must be returned, not slept", resp.StatusCode, waits)
	}
}

func TestDoLeavesAPermissionRefusalAlone(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := (&Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("a plain 403 was retried %d times", calls.Load()-1)
	}
}
