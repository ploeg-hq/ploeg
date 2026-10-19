package shiftengine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Shift 163 (Work Item 188) closed writing_run_failed_repeatedly within two
// minutes: OpenHands rejected the MCP server in its session configuration
// three times, and each attempt was charged to the ticket.
func TestHarnessConfig_ParksAtTheFirstOccurrenceAndQuotesTheHarness(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := runThroughReaders(t, e, "1880")

	failTheRunFor(t, claimTheBuilder(t), work.FailureHarnessConfig,
		"acp agent rejected its configuration: Error: 6 validation errors for MCPConfig mcpServers.litellm")
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	_, closed, reason := shiftRow(t, shiftID)
	if !closed || reason != reasonHarnessConfig {
		t.Fatalf("shift closed=%v reason=%q, want closed %q at the first occurrence", closed, reason, reasonHarnessConfig)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human", got)
	}
	if retry, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0); retry != nil {
		t.Fatal("the builder was retried against a harness that rejects its configuration every time")
	}
	detail := auditDetail(t, itemID, "work_item.needs_human")
	for _, want := range []string{"system configuration problem, not the ticket", "Error: 6 validation errors for MCPConfig mcpServers.litellm"} {
		if !strings.Contains(detail, want) {
			t.Errorf("park reason %s does not say %q", detail, want)
		}
	}
}

func claimTheBuilder(t *testing.T) string {
	t.Helper()
	run, err := testStore.ClaimRole(context.Background(), "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	return run.RunToken
}
