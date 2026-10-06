package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestRunContext_EveryLineAfterClaimCarriesTheRunKeys(t *testing.T) {
	outcomes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/claim":
			_ = json.NewEncoder(rw).Encode(ClaimResponse{
				RunToken: "abc123def456ff00", ControlToken: "ctl",
				Deadline: time.Now().Add(time.Minute),
				WorkItem: work.WorkItem{ID: "wi-138", ExternalID: "VIK-138", Title: "t"},
				Shift:    7, Role: "builder", Round: 2, Writes: true,
			})
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			outcomes++
			rw.WriteHeader(http.StatusOK)
		default:
			rw.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	var logs bytes.Buffer
	w := &Worker{
		Cfg:     Config{Team: "bronze", Role: "builder"},
		API:     &APIClient{Base: srv.URL, HC: srv.Client(), BootstrapToken: "boot", WorkerID: "w1"},
		Adapter: fakeAgentAdapter(t, 0),
		Broker:  &recordingBroker{key: "sk-test"},
		Log:     slog.New(slog.NewTextHandler(&logs, nil)),
	}
	if err := w.RunContext(context.Background()); err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	if outcomes != 1 {
		t.Fatalf("outcome reports = %d, want 1", outcomes)
	}

	claimed := false
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, `msg="claimed work item"`) {
			claimed = true
		}
		if !claimed {
			continue
		}
		for _, key := range []string{"trace=ploeg-", "work_item=wi-138", "external_id=VIK-138", "team=bronze", "role=builder", "shift=7", "round=2"} {
			if !strings.Contains(line, key) {
				t.Errorf("log line lacks %q: %s", key, line)
			}
		}
	}
	if !claimed {
		t.Fatalf("no claim line logged:\n%s", logs.String())
	}
}
