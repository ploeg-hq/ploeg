package github

import (
	"testing"
)

func TestParseWebhook_AReviewCarriesGitHubsReviewID(t *testing.T) {
	_, p := newFake(t)
	for _, c := range []struct {
		review map[string]any
		want   string
	}{
		{review: map[string]any{"id": 80123, "state": "changes_requested", "body": "please fix"}, want: "80123"},
		{review: map[string]any{"state": "changes_requested", "body": "please fix"}, want: ""},
	} {
		payload := pullPayload("submitted", map[string]any{"number": 7, "state": "open",
			"head": map[string]any{"ref": "agent/vik-1", "sha": "h1"}})
		payload["review"] = c.review
		events, err := p.ParseWebhook(signed(t, p, "pull_request_review", "d", payload))
		if err != nil || len(events) != 1 || events[0].ReviewID != c.want {
			t.Errorf("review %v: events %+v err %v; want review id %q", c.review, events, err, c.want)
		}
	}
}
