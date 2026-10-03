package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestPullRequestChange_ReadsTitleLabelsFilesAndCommits(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/v1/repos/webgrip/ploeg/pulls/7":
			fmt.Fprint(w, `{"title":"Revert \"Add cards\"","body":"Reverts webgrip/ploeg#5","labels":[{"name":"hotfix"},{"name":""}]}`)
		case "/api/v1/repos/webgrip/ploeg/pulls/7/files":
			if r.URL.Query().Get("page") != "1" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[{"filename":"a.go","additions":12,"deletions":3},{"filename":"new.go","previous_filename":"old.go","additions":1,"deletions":0},`+
				`{"filename":"a.go"},{"filename":"bin.dat"},{"filename":"odd.go","additions":-1,"deletions":2}]`)
		case "/api/v1/repos/webgrip/ploeg/pulls/7/commits":
			if r.URL.Query().Get("files") != "false" {
				t.Errorf("commits read with files: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[{"commit":{"message":"Revert \"Add cards\"\n\nThis reverts commit abcdef1234567."}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).PullRequestChange(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := provider.PullRequestChange{Title: `Revert "Add cards"`, Body: "Reverts webgrip/ploeg#5", Labels: []string{"hotfix"},
		Files: []string{"a.go", "new.go", "old.go", "bin.dat", "odd.go"}, Commits: []string{"Revert \"Add cards\"\n\nThis reverts commit abcdef1234567."},
		Lines: map[string]provider.FileLines{"a.go": {Additions: 12, Deletions: 3}, "new.go": {Additions: 1}, "old.go": {}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("change = %+v; want %+v", got, want)
	}
	for _, a := range auth {
		if a != "token tok" {
			t.Fatalf("a read went out as %q", a)
		}
	}
}

func TestPullRequestChange_StopsAtTheFileBound(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			pages++
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			files := make([]string, 0, changePageSize)
			for i := 0; i < changePageSize; i++ {
				files = append(files, fmt.Sprintf(`{"filename":"f%d-%d.go"}`, page, i))
			}
			fmt.Fprintf(w, "[%s]", strings.Join(files, ","))
		case strings.HasSuffix(r.URL.Path, "/commits"):
			fmt.Fprint(w, `[]`)
		default:
			fmt.Fprint(w, `{"title":"big"}`)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestChange(context.Background(), "webgrip/ploeg", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != provider.MaxChangedFiles || !got.FilesTruncated || pages != provider.MaxChangedFiles/changePageSize+1 {
		t.Fatalf("files = %d truncated %v after %d pages; want %d, true, %d", len(got.Files), got.FilesTruncated, pages,
			provider.MaxChangedFiles, provider.MaxChangedFiles/changePageSize+1)
	}
}

func TestPullRequestChange_FailsOnAForgeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"title":"x"}`)
	}))
	defer srv.Close()
	if _, err := (&Provider{BaseURL: srv.URL}).PullRequestChange(context.Background(), "webgrip/ploeg", 9); err == nil {
		t.Fatal("a failed file read reported no error")
	}
}
