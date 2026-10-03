package harness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func sampleVerification() *Verification {
	started := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	finished := started.Add(40 * time.Second)
	zero, two := 0, 2
	return &Verification{
		Result: VerificationFailed, Commit: strings.Repeat("b", 40), Dirty: true,
		Stopped: "an earlier check failed", StartedAt: started, FinishedAt: finished,
		Checks: []VerificationCheck{
			{Command: "go vet ./...", Result: VerificationPassed, ExitCode: &zero, StartedAt: &started, FinishedAt: &started},
			{Command: "go test ./...", Result: VerificationFailed, ExitCode: &two, StartedAt: &started, FinishedAt: &finished},
			{Command: "gofmt -l .", Result: VerificationCheckNotRun},
		},
	}
}

func TestOutcomeReportVerificationMatchesSchema(t *testing.T) {
	sch := compileSchema(t, "outcomereport.v1.schema.json")
	report := OutcomeReport{Outcome: work.OutcomePROpened, Summary: "opened", Verification: sampleVerification()}
	if err := validate(t, sch, report); err != nil {
		t.Fatalf("OutcomeReport with a verification does not validate: %v", err)
	}
	if err := report.Verification.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"short commit":   func(v map[string]any) { v["commit"] = "bbbbbbbbbbbb" },
		"unknown result": func(v map[string]any) { v["result"] = "green" },
		"unknown key":    func(v map[string]any) { v["claimedBy"] = "agent" },
		"no checks":      func(v map[string]any) { delete(v, "checks") },
	} {
		raw, _ := json.Marshal(report)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		mutate(doc["verification"].(map[string]any))
		if err := validate(t, sch, doc); err == nil {
			t.Errorf("verification with %s validated", name)
		}
	}
}

func TestOutcomeReportWithoutVerificationStillDecodes(t *testing.T) {
	var r OutcomeReport
	if err := json.Unmarshal([]byte(`{"outcome":"pr_opened","summary":"opened [Ploeg verification passed]"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Verification != nil {
		t.Errorf("a report from an older worker decoded with a verification: %+v", r.Verification)
	}
}

func TestVerificationValidateRejectsInconsistentRecords(t *testing.T) {
	for name, mutate := range map[string]func(*Verification){
		"passed with a failed check": func(v *Verification) { v.Result = VerificationPassed },
		"incomplete with a failure":  func(v *Verification) { v.Result = VerificationIncomplete },
		"failed without a failure":   func(v *Verification) { v.Checks = v.Checks[:1] },
		"passed with a check not run": func(v *Verification) {
			v.Checks = []VerificationCheck{v.Checks[0], v.Checks[2]}
			v.Result = VerificationPassed
		},
		"ran without an exit code": func(v *Verification) { v.Checks[0].ExitCode = nil },
		"abbreviated commit":       func(v *Verification) { v.Commit = "bbbbbbbbbbbb" },
		"unknown result":           func(v *Verification) { v.Result = "unknown" },
		"unknown check result":     func(v *Verification) { v.Checks[2].Result = "skipped" },
	} {
		v := sampleVerification()
		mutate(v)
		if err := v.Validate(); err == nil {
			t.Errorf("%s validated", name)
		}
	}
}
