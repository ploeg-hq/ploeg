package worker

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func forgejoPulls(from, n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"html_url":"https://forge/x/y/pulls/%d","head":{"ref":"other-%d"},"base":{"ref":"main"}}`, from+i, from+i)
	}
	return "[" + strings.Join(items, ",") + "]"
}

func pagedForgejo(t *testing.T, total int, pages map[int]string, failPage int) (string, *[]int) {
	t.Helper()
	var requested []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		requested = append(requested, page)
		if r.URL.Query().Get("limit") != strconv.Itoa(forgejoPullsPageSize) {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		if page == failPage {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if total >= 0 {
			w.Header().Set("X-Total-Count", strconv.Itoa(total))
		}
		body, ok := pages[page]
		if !ok {
			body = `[]`
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &requested
}

func TestFindOpenChangeRequestForgejoFindsAPullRequestOnPageTwo(t *testing.T) {
	match := `[{"html_url":"https://forge/x/y/pulls/77","head":{"ref":"ploeg/VIK-1"},"base":{"ref":"main"}}]`
	base, requested := pagedForgejo(t, forgejoPullsPageSize+1, map[int]string{
		1: forgejoPulls(1, forgejoPullsPageSize),
		2: match,
	}, 0)
	ref := harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y", BaseBranch: "main"}
	got, err := findOpenChangeRequestURL(ref, "tok", "ploeg/VIK-1")
	if err != nil {
		t.Fatalf("findOpenChangeRequest: %v", err)
	}
	if got != "https://forge/x/y/pulls/77" {
		t.Errorf("url = %q, want the page-two pull request", got)
	}
	if fmt.Sprint(*requested) != "[1 2]" {
		t.Errorf("pages requested = %v, want [1 2]", *requested)
	}
}

func TestFindOpenChangeRequestForgejoPagesWithoutATotalCount(t *testing.T) {
	match := `[{"html_url":"https://forge/x/y/pulls/88","head":{"ref":"b"},"base":{"ref":"main"}}]`
	base, _ := pagedForgejo(t, -1, map[int]string{
		1: forgejoPulls(1, forgejoPullsPageSize),
		2: match,
	}, 0)
	ref := harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y"}
	got, err := findOpenChangeRequestURL(ref, "tok", "b")
	if err != nil || got != "https://forge/x/y/pulls/88" {
		t.Fatalf("url = %q, err = %v; want the page-two pull request", got, err)
	}
}

func TestFindOpenChangeRequestForgejoStopsWhenEveryPullRequestIsSeen(t *testing.T) {
	base, requested := pagedForgejo(t, forgejoPullsPageSize+3, map[int]string{
		1: forgejoPulls(1, forgejoPullsPageSize),
		2: forgejoPulls(forgejoPullsPageSize+1, 3),
	}, 0)
	ref := harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y"}
	got, err := findOpenChangeRequestURL(ref, "tok", "absent")
	if err != nil || got != "" {
		t.Fatalf("url = %q, err = %v; want no pull request and no error", got, err)
	}
	if fmt.Sprint(*requested) != "[1 2]" {
		t.Errorf("pages requested = %v, want [1 2]", *requested)
	}
}

func TestFindOpenChangeRequestForgejoFailedPageIsAnError(t *testing.T) {
	base, _ := pagedForgejo(t, 3*forgejoPullsPageSize, map[int]string{
		1: forgejoPulls(1, forgejoPullsPageSize),
	}, 2)
	ref := harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y"}
	if got, err := findOpenChangeRequestURL(ref, "tok", "b"); err == nil {
		t.Fatalf("url = %q, err = nil; want an error when a page fails", got)
	}
}

func TestFindOpenChangeRequestForgejoPageCapIsAnError(t *testing.T) {
	pages := map[int]string{}
	for p := 1; p <= forgejoPullsMaxPages+1; p++ {
		pages[p] = forgejoPulls((p-1)*forgejoPullsPageSize+1, forgejoPullsPageSize)
	}
	base, requested := pagedForgejo(t, -1, pages, 0)
	ref := harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y"}
	_, err := findOpenChangeRequestURL(ref, "tok", "b")
	if !errors.Is(err, errTooManyOpenPullRequests) {
		t.Fatalf("err = %v, want errTooManyOpenPullRequests", err)
	}
	if len(*requested) != forgejoPullsMaxPages {
		t.Errorf("pages requested = %d, want %d", len(*requested), forgejoPullsMaxPages)
	}
}
