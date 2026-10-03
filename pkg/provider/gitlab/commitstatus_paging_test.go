package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func pagedStatuses(t *testing.T, pages []string, failPage int) (*httptest.Server, *[]int) {
	t.Helper()
	var requested []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		requested = append(requested, page)
		if page == failPage {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if page < 1 || page > len(pages) {
			w.Header().Set("X-Next-Page", "")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		next := ""
		if page < len(pages) {
			next = strconv.Itoa(page + 1)
		}
		w.Header().Set("X-Next-Page", next)
		_, _ = w.Write([]byte(pages[page-1]))
	}))
	t.Cleanup(srv.Close)
	return srv, &requested
}

func TestCommitStatus_AFailedCheckOnPageTwoFailsTheCommit(t *testing.T) {
	srv, requested := pagedStatuses(t, []string{
		`[{"id":1,"name":"verify","status":"success"}]`,
		`[{"id":2,"name":"lint","status":"failed"}]`,
	}, 0)
	status, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "g/p", "abc")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if status.State != provider.CommitFailure || len(status.Checks) != 2 {
		t.Errorf("status = %+v, want failure from the page-two check", status)
	}
	if len(*requested) != 2 || (*requested)[0] != 1 || (*requested)[1] != 2 {
		t.Errorf("pages requested = %v, want [1 2]", *requested)
	}
}

func TestCommitStatus_APendingCheckOnPageTwoKeepsTheCommitPending(t *testing.T) {
	srv, _ := pagedStatuses(t, []string{
		`[{"id":1,"name":"verify","status":"success"}]`,
		`[{"id":2,"name":"e2e","status":"running"}]`,
	}, 0)
	status, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "g/p", "abc")
	if err != nil || !ok || status.State != provider.CommitPending {
		t.Fatalf("status = %+v, ok = %v, err = %v; want pending", status, ok, err)
	}
}

func TestCommitStatus_FollowsAFullPageWithoutANextPageHeader(t *testing.T) {
	full := make([]string, notesPerPage)
	for i := range full {
		full[i] = `{"id":` + strconv.Itoa(i+1) + `,"name":"job` + strconv.Itoa(i) + `","status":"success"}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte("[" + strings.Join(full, ",") + "]"))
			return
		}
		_, _ = w.Write([]byte(`[{"id":1000,"name":"late","status":"failed"}]`))
	}))
	defer srv.Close()
	status, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "g/p", "abc")
	if err != nil || !ok || status.State != provider.CommitFailure || len(status.Checks) != notesPerPage+1 {
		t.Fatalf("state = %s, checks = %d, ok = %v, err = %v", status.State, len(status.Checks), ok, err)
	}
}

func TestCommitStatus_TheNewestStatusOfARepeatedCheckCounts(t *testing.T) {
	srv, _ := pagedStatuses(t, []string{
		`[{"id":5,"name":"verify","status":"success"},{"id":1,"name":"lint","status":"failed"}]`,
		`[{"id":3,"name":"verify","status":"failed"},{"id":9,"name":"lint","status":"success"}]`,
	}, 0)
	status, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "g/p", "abc")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if status.State != provider.CommitSuccess || len(status.Checks) != 2 {
		t.Errorf("status = %+v, want success from the newest status of each check", status)
	}
}

func TestCommitStatus_AFailedPageIsAnErrorNotAPartialSuccess(t *testing.T) {
	srv, _ := pagedStatuses(t, []string{
		`[{"id":1,"name":"verify","status":"success"}]`,
		`[]`,
	}, 2)
	status, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "g/p", "abc")
	if err == nil || ok || status.State != "" {
		t.Fatalf("status = %+v, ok = %v, err = %v; want an error and no state", status, ok, err)
	}
}
