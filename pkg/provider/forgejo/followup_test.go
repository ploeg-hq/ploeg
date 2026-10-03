package forgejo

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestParseWebhook_ReviewCarriesVerdictAndReviewer(t *testing.T) {
	p := &Provider{Secret: "shh"}
	for _, tc := range []struct {
		kind string
		want provider.ForgeReviewState
	}{
		{"pull_request_review_rejected", provider.ForgeReviewChangesRequested},
		{"pull_request_review_approved", provider.ForgeReviewApproved},
		{"pull_request_review_comment", provider.ForgeReviewCommented},
	} {
		events, err := post(t, p, tc.kind, map[string]any{
			"repository":   map[string]any{"full_name": "webgrip/ploeg"},
			"pull_request": map[string]any{"number": 12, "head": map[string]any{"ref": "agent/vik-585"}},
			"review":       map[string]any{"type": tc.kind, "content": "x"},
			"sender":       map[string]any{"login": "ryan"},
		})
		if err != nil || len(events) != 1 {
			t.Fatalf("%s: events = %+v, err = %v", tc.kind, events, err)
		}
		if events[0].Review != tc.want || events[0].Actor != "ryan" {
			t.Errorf("%s: event = %+v, want review %q by ryan", tc.kind, events[0], tc.want)
		}
	}
}

func TestParseWebhook_CheckFailedAcceptsBranchObjects(t *testing.T) {
	p := &Provider{Secret: "shh"}
	events, err := post(t, p, "status", map[string]any{
		"repository":  map[string]any{"full_name": "webgrip/ploeg"},
		"state":       "failure",
		"context":     "ci/test",
		"description": "go vet failed",
		"branches":    []any{map[string]any{"name": "agent/vik-585"}},
	})
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(events) != 1 || events[0].Branch != "agent/vik-585" || !strings.Contains(events[0].Body, "go vet failed") {
		t.Fatalf("event = %+v", events)
	}
}
