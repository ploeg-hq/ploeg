package gitlab

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

func TestChangedPaths_ReadsEachDiffWithItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("read without the token: %s", r.URL)
		}
		if r.URL.EscapedPath() != "/api/v4/projects/group%2Fsub%2Fapp/merge_requests/4/diffs" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `[{"new_path":"AGENTS.md","old_path":"AGENTS.md"},`+
			`{"new_path":"new.go","old_path":"new.go","new_file":true},`+
			`{"new_path":"gone.go","old_path":"gone.go","deleted_file":true},`+
			`{"new_path":".claude/settings.json","old_path":"settings.json","renamed_file":true},`+
			`{"new_path":"","old_path":""}]`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).ChangedPaths(context.Background(), "group/sub/app", 4)
	if err != nil {
		t.Fatal(err)
	}
	want := provider.ChangedPaths{Paths: []provider.ChangedPath{
		{Path: "AGENTS.md", Status: provider.PathModified},
		{Path: "new.go", Status: provider.PathAdded},
		{Path: "gone.go", Status: provider.PathDeleted},
		{Path: ".claude/settings.json", Status: provider.PathRenamed, PreviousPath: "settings.json"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %+v; want %+v", got, want)
	}
}

func TestChangedPaths_StopsAtTheCapAndSaysSo(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		diffs := make([]string, 0, changePageSize)
		for i := 0; i < changePageSize; i++ {
			diffs = append(diffs, fmt.Sprintf(`{"new_path":"f%d-%d.go","old_path":"f%d-%d.go"}`, page, i, page, i))
		}
		fmt.Fprint(w, "["+strings.Join(diffs, ",")+"]")
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).ChangedPaths(context.Background(), "group/app", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != provider.MaxChangedPaths || !got.Truncated || pages != provider.MaxChangedPaths/changePageSize+1 {
		t.Errorf("paths %d truncated %v after %d pages; want %d, true and one page past the cap",
			len(got.Paths), got.Truncated, pages, provider.MaxChangedPaths)
	}
}

func TestChangedPaths_FailsOnAForgeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := (&Provider{BaseURL: srv.URL}).ChangedPaths(context.Background(), "group/app", 5); err == nil {
		t.Fatal("a forge error must fail the read, never report an empty list")
	}
}
