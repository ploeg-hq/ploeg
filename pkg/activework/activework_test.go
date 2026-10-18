package activework

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

type fakeReader struct {
	own    store.WorkPaths
	others []store.ActiveShift
}

func (f fakeReader) WorkItemPaths(context.Context, int64) (store.WorkPaths, error) { return f.own, nil }
func (f fakeReader) ActiveWork(context.Context, int64) ([]store.ActiveShift, error) {
	return f.others, nil
}

func TestCoversMatchesFilesAndDirectories(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"pkg/store/a.go", "pkg/store/a.go", true},
		{"pkg/store/", "pkg/store/a.go", true},
		{"pkg/store/a.go", "pkg/store/", true},
		{"pkg/store", "pkg/store/a.go", false},
		{"pkg/store/a.go", "pkg/store/b.go", false},
		{"pkg/st/", "pkg/store/a.go", false},
	} {
		if got := Covers(c.a, c.b); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestReportListsOverlappingShiftsFirstAndNamesTheSharedPaths(t *testing.T) {
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	r, err := For(context.Background(), fakeReader{
		own: store.WorkPaths{Owner: "webgrip", Repo: "ploeg", Changed: []string{"pkg/store/store.go"}, ChangedKnown: true, Planned: []string{"docs/"}},
		others: []store.ActiveShift{
			{WorkItemID: "3", Provider: "vikunja", ExternalID: "30", Title: "Older, unrelated", Team: "bronze", Branch: "agent/vik-30", OpenedAt: at,
				Paths: store.WorkPaths{Changed: []string{"cmd/ploegd/main.go"}, ChangedKnown: true}},
			{WorkItemID: "4", Provider: "vikunja", ExternalID: "40", Title: "Touches the store", Team: "silver", Branch: "agent/vik-40", OpenedAt: at.Add(time.Hour),
				Paths: store.WorkPaths{PullRequest: 9, Changed: []string{"pkg/store/store.go", "docs/index.md"}, ChangedKnown: true}},
			{WorkItemID: "5", Provider: "vikunja", ExternalID: "50", Title: "Just started", Team: "bronze", Branch: "agent/vik-50", OpenedAt: at.Add(2 * time.Hour)},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Shifts) != 3 || r.Shifts[0].WorkItemID != "4" || r.Shifts[1].WorkItemID != "3" || r.Shifts[2].WorkItemID != "5" {
		t.Fatalf("order = %+v", r.Shifts)
	}
	if got := r.Shifts[0].Shared; len(got) != 2 || got[0] != "pkg/store/store.go" || got[1] != "docs/" {
		t.Errorf("shared = %v", got)
	}
	if len(r.Overlapping()) != 1 || r.Shifts[2].PathsKnown {
		t.Errorf("overlapping = %d, unknown paths known = %v", len(r.Overlapping()), r.Shifts[2].PathsKnown)
	}
	md := r.Markdown(0)
	for _, want := range []string{"Other active work in webgrip/ploeg (3 Shift(s); 1 share a path", `VIK-40 "Touches the store"`,
		"pull request #9. Shares: pkg/store/store.go, docs/", "Touches: cmd/ploegd/main.go", "Paths not known yet"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if short := r.Markdown(200); len(short) > 200 || !strings.Contains(short, "VIK-40") || strings.Contains(short, "VIK-50") {
		t.Errorf("bounded markdown = %q", short)
	}
}

func TestAWorkItemWithoutARepositoryHasNoActiveWork(t *testing.T) {
	r, err := For(context.Background(), fakeReader{others: []store.ActiveShift{{WorkItemID: "4"}}}, 1)
	if err != nil || len(r.Shifts) != 0 || r.Markdown(0) != "" {
		t.Fatalf("report = %+v, %v", r, err)
	}
}

func TestListedPathsAreBounded(t *testing.T) {
	var many []string
	for i := range 15 {
		many = append(many, "f"+string(rune('a'+i)))
	}
	r := Compare(store.WorkPaths{Owner: "o", Repo: "r"}, []store.ActiveShift{{Paths: store.WorkPaths{Changed: many, ChangedKnown: true}}})
	if s := r.Shifts[0]; len(s.Paths) != 10 || s.MorePaths != 5 {
		t.Fatalf("paths = %d, more = %d", len(s.Paths), s.MorePaths)
	}
}
