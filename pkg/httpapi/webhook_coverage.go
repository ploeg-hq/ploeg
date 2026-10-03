package httpapi

import (
	"slices"
	"sync"
	"time"
)

// WebhookCoverage is the latest check of whether each configured tracker
// project delivers assignment events to this Ploeg. /readyz reports it
// without failing readiness, because a missing webhook stops dispatch for
// one board, not the whole service.
type WebhookCoverage struct {
	mu        sync.Mutex
	checked   int
	missing   []string
	failed    []string
	checkedAt time.Time
}

// Record replaces the coverage with a completed check.
func (c *WebhookCoverage) Record(checked int, missing, failed []string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checked = checked
	c.missing = slices.Clone(missing)
	c.failed = slices.Clone(failed)
	c.checkedAt = at
}

// Missing returns the projects whose assignment webhook was not found.
func (c *WebhookCoverage) Missing() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.missing)
}

type coverageCounts struct {
	missing, failed int
	at              time.Time
}

func (c *WebhookCoverage) snapshot() (coverageCounts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return coverageCounts{missing: len(c.missing), failed: len(c.failed), at: c.checkedAt}, !c.checkedAt.IsZero()
}

func (c *WebhookCoverage) report() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.checkedAt.IsZero() {
		return map[string]any{"status": "pending"}
	}
	status := "complete"
	if len(c.missing) > 0 || len(c.failed) > 0 {
		status = "degraded"
	}
	return map[string]any{
		"status":            status,
		"checkedProjects":   c.checked,
		"missingProjects":   len(c.missing),
		"uncheckedProjects": len(c.failed),
		"checkedAt":         c.checkedAt.UTC().Format(time.RFC3339),
	}
}
