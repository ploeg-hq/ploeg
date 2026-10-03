package sandboxlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const claimPath = "/apis/extensions.agents.x-k8s.io/v1beta1/namespaces/ploeg/sandboxclaims"

type fakeAPI struct {
	mu        sync.Mutex
	creates   []map[string]any
	deletes   int
	gets      int
	createErr int
	states    []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == claimPath:
		if f.createErr != 0 {
			w.WriteHeader(f.createErr)
			_, _ = io.WriteString(w, `{"message":"denied"}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.creates = append(f.creates, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodGet && r.URL.Path == claimPath+"/worker-abc":
		state := "running"
		if f.gets < len(f.states) {
			state = f.states[f.gets]
		} else if len(f.states) > 0 {
			state = f.states[len(f.states)-1]
		}
		f.gets++
		switch state {
		case "gone":
			w.WriteHeader(http.StatusNotFound)
		case "finished":
			_, _ = io.WriteString(w, `{"status":{"conditions":[{"type":"Ready","status":"False"},{"type":"Finished","status":"True","reason":"PodSucceeded"}]}}`)
		case "unready":
			_, _ = io.WriteString(w, `{"status":{"conditions":[{"type":"Ready","status":"False","reason":"ReconcilerError","message":"pod is owned by Job/x"}]}}`)
		default:
			_, _ = io.WriteString(w, `{"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
		}
	case r.Method == http.MethodDelete && r.URL.Path == claimPath+"/worker-abc":
		f.deletes++
		_, _ = io.WriteString(w, `{}`)
	default:
		w.WriteHeader(http.StatusTeapot)
	}
}

func config(t *testing.T, api *fakeAPI) Config {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return Config{
		APIBase:                 srv.URL,
		Token:                   "test-token",
		HTTPClient:              srv.Client(),
		Namespace:               "ploeg",
		ClaimName:               "worker-abc",
		JobName:                 "ploeg-bronze-12345",
		JobUID:                  "4f1c0d6e-0000-4000-8000-000000000001",
		WarmPool:                "ploeg-bronze",
		RunDeadline:             2 * time.Hour,
		ShutdownMargin:          10 * time.Minute,
		TTLSecondsAfterFinished: 60,
		PollInterval:            time.Millisecond,
		Now:                     func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) },
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLaunchCreatesOneClaimWithBackstops(t *testing.T) {
	api := &fakeAPI{states: []string{"running", "finished"}}
	if err := Launch(context.Background(), config(t, api), quiet()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if len(api.creates) != 1 {
		t.Fatalf("claims created = %d, want exactly 1", len(api.creates))
	}
	got, _ := json.Marshal(api.creates[0])
	for _, want := range []string{
		`"kind":"SandboxClaim"`,
		`"apiVersion":"extensions.agents.x-k8s.io/v1beta1"`,
		`"name":"worker-abc"`,
		`"warmPoolRef":{"name":"ploeg-bronze"}`,
		`"shutdownPolicy":"Delete"`,
		`"shutdownTime":"2026-09-26T14:10:00Z"`,
		`"ttlSecondsAfterFinished":60`,
		`"kind":"Job","name":"ploeg-bronze-12345"`,
		`"uid":"4f1c0d6e-0000-4000-8000-000000000001"`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("claim body lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), `"env"`) {
		t.Errorf("claim must not inject environment:\n%s", got)
	}
}

func TestLaunchDeletesTheClaimWhenFinished(t *testing.T) {
	api := &fakeAPI{states: []string{"finished"}}
	if err := Launch(context.Background(), config(t, api), quiet()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if api.deletes != 1 {
		t.Fatalf("deletes = %d, want 1: a finished claim left in place lets the controller recreate a deleted pod", api.deletes)
	}
}

func TestLaunchEndsWhenTheClaimDisappears(t *testing.T) {
	api := &fakeAPI{states: []string{"running", "gone"}}
	if err := Launch(context.Background(), config(t, api), quiet()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if api.deletes != 0 || len(api.creates) != 1 {
		t.Fatalf("creates=%d deletes=%d, want 1 and 0", len(api.creates), api.deletes)
	}
}

func TestLaunchDoesNotRetryAFailedCreate(t *testing.T) {
	api := &fakeAPI{createErr: http.StatusForbidden}
	err := Launch(context.Background(), config(t, api), quiet())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Launch error = %v, want the 403", err)
	}
	if api.gets != 0 {
		t.Fatalf("launcher polled %d times after a failed create", api.gets)
	}
}

func TestLaunchLeavesTheClaimWhenItsContextEnds(t *testing.T) {
	api := &fakeAPI{states: []string{"running"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := Launch(ctx, config(t, api), quiet()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if api.deletes != 0 {
		t.Fatalf("a stopping launcher deleted a running claim; that would kill a healthy Run")
	}
	if len(api.creates) != 1 {
		t.Fatalf("claims created = %d, want exactly 1", len(api.creates))
	}
}

func TestLaunchRejectsIncompleteConfiguration(t *testing.T) {
	cfg := config(t, &fakeAPI{})
	cfg.JobUID = ""
	if err := Launch(context.Background(), cfg, quiet()); err == nil || !strings.Contains(err.Error(), "job UID") {
		t.Fatalf("Launch error = %v, want a missing job UID", err)
	}
}

func TestClaimNameForNeverReusesTheLauncherPodName(t *testing.T) {
	long := "ploeg-worker-bronze-builder-abcdefghijklmnopqrstuvwxyz0-x7k2p"
	for _, tc := range []struct{ pod, want string }{
		{"ploeg-worker-copper-nx7bg-5hxpx", "sbx-ploeg-worker-copper-nx7bg-5hxpx"},
		{long, "sbx-" + long[len(long)-59:]},
		{"", ""},
	} {
		got := ClaimNameFor(tc.pod)
		if got != tc.want {
			t.Errorf("ClaimNameFor(%q) = %q, want %q", tc.pod, got, tc.want)
		}
		if tc.pod != "" && (got == tc.pod || len(got) > 63 || !strings.HasSuffix(tc.pod, strings.TrimPrefix(got, "sbx-"))) {
			t.Errorf("ClaimNameFor(%q) = %q collides, overflows or drops the pod's unique tail", tc.pod, got)
		}
	}
}

func TestLaunchDeletesAClaimThatNeverBecomesReady(t *testing.T) {
	api := &fakeAPI{states: []string{"unready"}}
	cfg := config(t, api)
	cfg.StartTimeout = 10 * time.Minute
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cfg.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	err := Launch(context.Background(), cfg, quiet())
	if !errors.Is(err, ErrNeverReady) || !strings.Contains(err.Error(), "ReconcilerError") {
		t.Fatalf("Launch() = %v, want ErrNeverReady naming the controller's reason", err)
	}
	if api.deletes != 1 || len(api.creates) != 1 {
		t.Fatalf("creates=%d deletes=%d, want one claim created and deleted", len(api.creates), api.deletes)
	}
}

func TestLaunchKeepsAReadyClaimPastTheStartTimeout(t *testing.T) {
	api := &fakeAPI{states: []string{"running", "running", "running", "finished"}}
	cfg := config(t, api)
	cfg.StartTimeout = time.Minute
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cfg.Now = func() time.Time { clock = clock.Add(time.Hour); return clock }
	if err := Launch(context.Background(), cfg, quiet()); err != nil {
		t.Fatalf("Launch() = %v, want a ready claim to run to completion", err)
	}
	if api.deletes != 1 {
		t.Fatalf("deletes=%d, want the finished claim deleted once", api.deletes)
	}
}
