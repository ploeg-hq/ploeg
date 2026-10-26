package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	seededTitle         = "Rotate the payroll database password for ACME-SEED-TITLE"
	seededSummary       = "SEED-SUMMARY the agent wrote about the customer"
	seededHarnessOutput = "SEED-HARNESS-OUTPUT cat .env"
	seededSecret        = "ghp_SEEDsecretSEEDsecretSEEDsecretSEED1234"
	seededLinkQuery     = "access_token=SEED-LINK-QUERY"
)

func talkativeHarness(t *testing.T) harness.Adapter {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "talkative-agent.sh")
	report, err := json.Marshal(map[string]any{
		"outcome": "stuck",
		"summary": seededSummary,
		"links":   []string{"https://forge.example/acme/app/pulls/7?" + seededLinkQuery},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n" +
		"echo '" + seededHarnessOutput + " " + seededSecret + "'\n" +
		"echo '" + seededHarnessOutput + " stderr " + seededSecret + "' >&2\n" +
		"printf '%s' '" + string(report) + "' > \"$PLOEG_OUTCOME_FILE\"\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return harness.RunCommand(openhands.New(bin))
}

func captureProcessOutput(t *testing.T) func() string {
	t.Helper()
	stdout, stderr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	restored := false
	restore := func() string {
		if restored {
			return ""
		}
		restored = true
		os.Stdout, os.Stderr = stdout, stderr
		_ = w.Close()
		return <-done
	}
	t.Cleanup(func() { restore() })
	return restore
}

func TestRunLogsCarryNoRunTextAndTheStuckReasonIsRedacted(t *testing.T) {
	var reported harness.OutcomeReport
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/claim":
			_ = json.NewEncoder(rw).Encode(ClaimResponse{
				RunToken: "abc123def456ff00", ControlToken: "ctl",
				Deadline: time.Now().Add(time.Minute),
				WorkItem: work.WorkItem{ID: "wi-9", ExternalID: "VIK-9", Title: seededTitle, Description: "SEED-DESCRIPTION"},
				Shift:    3, Role: "reviewer", Round: 1, PreAuthor: true,
			})
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			_ = json.NewDecoder(r.Body).Decode(&reported)
			rw.WriteHeader(http.StatusOK)
		default:
			rw.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	var logs bytes.Buffer
	cfg := Config{Team: "bronze", Role: "reviewer", WorkDir: t.TempDir(),
		APIURL: srv.URL, ForgeURL: gitForge(t, map[string]string{"README.md": "# example\n"}),
		DefaultForge: harness.ForgeForgejo, BuilderToken: "tok", ForgeTokenAccess: ForgeTokenReadOnly,
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development"}
	w := New(cfg, talkativeHarness(t), llmbroker.Static{Key: "sk-test"}, slog.New(slog.NewTextHandler(&logs, nil)))
	processOutput := captureProcessOutput(t)
	runErr := w.RunContext(context.Background())
	printed := processOutput()
	if runErr != nil {
		t.Fatalf("RunContext: %v", runErr)
	}

	if reported.Outcome != work.OutcomeStuck {
		t.Fatalf("reported outcome = %q, want stuck", reported.Outcome)
	}
	if !strings.Contains(reported.StuckReason, seededHarnessOutput) {
		t.Fatalf("the stuck reason lost the harness output it is made of: %q", reported.StuckReason)
	}
	if strings.Contains(reported.StuckReason, seededSecret) {
		t.Errorf("the stuck reason carries a credential the redactor masks: %q", reported.StuckReason)
	}
	for where, text := range map[string]string{"worker log": logs.String(), "process stdout and stderr": printed} {
		for _, seeded := range []string{seededTitle, "SEED-DESCRIPTION", seededSummary, seededHarnessOutput, seededSecret, seededLinkQuery} {
			if strings.Contains(text, seeded) {
				t.Errorf("%s carries %q:\n%s", where, seeded, text)
			}
		}
	}
}

func TestLoggedLinksKeepTheirPathAndLoseTheirQueryAndCredentials(t *testing.T) {
	got := linksForLog([]string{
		"https://forge.example/acme/app/pulls/7?" + seededLinkQuery + "#frag",
		"https://user:" + seededSecret + "@forge.example/acme/app/commit/abc",
		"not a link " + seededSummary,
	})
	want := []string{"https://forge.example/acme/app/pulls/7", "https://forge.example/acme/app/commit/abc", "[unparsed link]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("linksForLog = %q, want %q", got, want)
	}
}

func TestHarnessOutputReachesThePodLogOnlyWhenOptedIn(t *testing.T) {
	for _, optedIn := range []bool{false, true} {
		var pod bytes.Buffer
		var tail harness.TailBuffer
		w := &Worker{Cfg: Config{HarnessOutputInLog: optedIn}}
		if _, err := w.harnessOutput(&pod, &tail).Write([]byte("harness line")); err != nil {
			t.Fatal(err)
		}
		if string(tail.Bytes()) != "harness line" {
			t.Errorf("opted in %v: tail = %q, want the line", optedIn, tail.Bytes())
		}
		if inPod := pod.String() == "harness line"; inPod != optedIn {
			t.Errorf("opted in %v: pod log = %q", optedIn, pod.String())
		}
	}
}
