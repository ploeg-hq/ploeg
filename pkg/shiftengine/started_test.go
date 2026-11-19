package shiftengine

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// --- unit: the message a Shift opening posts on the tracker ------------------

func TestStartedMessageExistsOnlyToCarryTheConfiguredLink(t *testing.T) {
	cases := []struct {
		name     string
		links    ReportLinks
		id       int64
		wantLink string
	}{
		{"no template, no message", ReportLinks{}, 314, ""},
		{"the id fills the template", ReportLinks{WorkItem: "https://console.example/items/{id}"}, 314, "https://console.example/items/314"},
		{"a template that needs a value this call lacks posts nothing", ReportLinks{WorkItem: "https://console.example/{team}/{id}"}, 314, ""},
		{"no Work Item id, no message", ReportLinks{WorkItem: "https://console.example/items/{id}"}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ok := startedMessage("bronze", workItemLink(tc.links, tc.id))
			if tc.wantLink == "" {
				if ok || body != "" {
					t.Fatalf("message = %q, want none", body)
				}
				return
			}
			if !ok {
				t.Fatal("no message, want one with the link")
			}
			for _, want := range []string{"Ploeg started working this item with Team bronze.", "**Follow it:** " + tc.wantLink} {
				if !strings.Contains(body, want) {
					t.Errorf("message lacks %q:\n%s", want, body)
				}
			}
		})
	}
}

// --- integration: the Shift opening tells the tracker ------------------------

// A deployment that names where a Work Item is followed gets one comment with
// that link when the Shift opens, beside the status write; the second
// EnsureShift on a live Shift posts nothing more.
func TestOpeningAShiftCommentsTheWorkItemLinkOnTheTrackerOnce(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	tracker := &fakeTracker{}
	e := newEngine(bronzePlan(6))
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	e.ReportLinks = ReportLinks{WorkItem: "https://console.example/items/{id}"}

	id, item := ingest(t, "bronze", "4711")
	for range 2 {
		if err := e.EnsureShift(ctx, id, item); err != nil {
			t.Fatalf("EnsureShift: %v", err)
		}
	}
	if len(tracker.statuses) != 1 || tracker.statuses[0] != work.StateActive {
		t.Fatalf("statuses = %v, want [active] once", tracker.statuses)
	}
	if len(tracker.comments) != 1 {
		t.Fatalf("comments = %q, want exactly one", tracker.comments)
	}
	if !strings.Contains(tracker.comments[0], "https://console.example/items/"+strconv.FormatInt(id, 10)) || !strings.Contains(tracker.comments[0], "Team bronze") {
		t.Errorf("comment does not follow the Work Item:\n%s", tracker.comments[0])
	}
}

// Without a template the tracker hears about the Shift through the status
// write alone, as before.
func TestOpeningAShiftWithoutALinkTemplatePostsNoComment(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	tracker := &fakeTracker{}
	e := newEngine(bronzePlan(6))
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}

	id, item := ingest(t, "bronze", "4712")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatalf("EnsureShift: %v", err)
	}
	if len(tracker.statuses) != 1 || tracker.statuses[0] != work.StateActive {
		t.Fatalf("statuses = %v, want [active] once", tracker.statuses)
	}
	if len(tracker.comments) != 0 {
		t.Fatalf("comments = %q, want none", tracker.comments)
	}
}
