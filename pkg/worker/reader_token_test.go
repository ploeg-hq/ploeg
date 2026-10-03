package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestReadingClaimIsRefusedWithoutAReadOnlyForgeToken(t *testing.T) {
	for _, access := range []string{"", ForgeTokenReadWrite} {
		w := &Worker{Cfg: Config{ForgeTokenAccess: access, BuilderToken: "read-write-canary"}, Log: discardLog()}
		claimed := &ClaimResponse{RunToken: "rt", Role: "reviewer", Writes: false,
			WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
		report := w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
		if report.Outcome != work.OutcomeStuck {
			t.Fatalf("access %q: outcome %q, want stuck", access, report.Outcome)
		}
		for _, want := range []string{`"reviewer"`, "read-only", "readTokenSecret"} {
			if !strings.Contains(report.StuckReason, want) {
				t.Errorf("access %q: stuck reason %q lacks %q", access, report.StuckReason, want)
			}
		}
		if strings.Contains(report.StuckReason+report.Summary, "read-write-canary") {
			t.Fatal("the refusal must not disclose the token")
		}
	}
}

func TestOnlyAReadingClaimNeedsAReadOnlyForgeToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		access string
		claim  ClaimResponse
	}{
		{"reader with a read-only token", ForgeTokenReadOnly, ClaimResponse{Role: "reviewer"}},
		{"writer with the read-write token", ForgeTokenReadWrite, ClaimResponse{Role: "builder", Writes: true}},
		{"writer outside the chart", "", ClaimResponse{Role: "builder", Writes: true}},
		{"pre-Shift claim", "", ClaimResponse{}},
	} {
		if _, refused := refuseReaderWithoutReadOnlyToken(Config{ForgeTokenAccess: tc.access}, &tc.claim); refused {
			t.Errorf("%s: refused", tc.name)
		}
	}
}
