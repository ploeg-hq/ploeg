package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var _ provider.MergeabilityReader = (*Provider)(nil)

func recordedPull(t *testing.T, file string) (*httptest.Server, *[]string) {
	t.Helper()
	body, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestPullRequestMergeability_DecodesARecordedForgejoResponse(t *testing.T) {
	srv, paths := recordedPull(t, "pull-45.json")
	m, err := (&Provider{BaseURL: srv.URL}).PullRequestMergeability(context.Background(), "webgrip/app", 45)
	if err != nil {
		t.Fatal(err)
	}
	if len(*paths) != 1 || (*paths)[0] != "GET /api/v1/repos/webgrip/app/pulls/45" {
		t.Errorf("requests = %v, want the one pulls read", *paths)
	}
	if m.Mergeable == nil || !*m.Mergeable {
		t.Errorf("mergeable = %v, want true", m.Mergeable)
	}
	if m.BaseBranch != "development" {
		t.Errorf("base branch = %q, want development", m.BaseBranch)
	}
	if m.Facts.State != provider.PullRequestMerged || m.Facts.HeadSHA != "35789c49649db7f902ebba838df0c75ea0cb4f15" {
		t.Errorf("facts = %+v", m.Facts)
	}
}

func TestPullRequestMergeability_ReportsAConflictedPullRequest(t *testing.T) {
	srv, _ := recordedPull(t, "pull-189-conflicted.json")
	m, err := (&Provider{BaseURL: srv.URL}).PullRequestMergeability(context.Background(), "webgrip/app", 189)
	if err != nil {
		t.Fatal(err)
	}
	if m.Mergeable == nil || *m.Mergeable {
		t.Errorf("mergeable = %v, want false", m.Mergeable)
	}
	if m.Facts.State != provider.PullRequestOpen || m.Facts.HeadSHA != "7b514cfed1979cf1bc1dac23e5fa131f51e86df6" ||
		m.BaseBranch != "development" {
		t.Errorf("mergeability = %+v", m)
	}
}

func TestPullRequestMergeability_LeavesAnUnreportedMergeableNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"open","merged":false,"head":{"sha":"abc"}}`))
	}))
	defer srv.Close()
	m, err := (&Provider{BaseURL: srv.URL}).PullRequestMergeability(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	if m.Mergeable != nil || m.BaseBranch != "" {
		t.Errorf("an unreported merge state became %v on %q", m.Mergeable, m.BaseBranch)
	}
}

func TestPullRequestMergeability_SurfacesAForgeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	m, err := (&Provider{BaseURL: srv.URL}).PullRequestMergeability(context.Background(), "webgrip/ploeg", 7)
	if err == nil {
		t.Fatalf("a 500 read as %+v", m)
	}
	if m.Mergeable != nil {
		t.Errorf("a failed read reported mergeable %v", *m.Mergeable)
	}
}
