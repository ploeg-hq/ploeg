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

func TestChangedPaths_ReadsEachPathWithItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token tok" {
			t.Errorf("read without the token: %s", r.URL)
		}
		if r.URL.Path != "/api/v1/repos/webgrip/ploeg/pulls/7/files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `[{"filename":"AGENTS.md","status":"changed"},{"filename":"new.go","status":"added"},`+
			`{"filename":"gone.go","status":"deleted"},{"filename":"CLAUDE.md","previous_filename":"docs/CLAUDE.md","status":"renamed"},`+
			`{"filename":"copy.go","previous_filename":"src.go","status":"copied"},{"filename":"mode.sh","status":"unchanged"},`+
			`{"filename":"","status":"added"},{"filename":"odd.go","status":"vibes"}]`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).ChangedPaths(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := provider.ChangedPaths{Paths: []provider.ChangedPath{
		{Path: "AGENTS.md", Status: provider.PathModified},
		{Path: "new.go", Status: provider.PathAdded},
		{Path: "gone.go", Status: provider.PathDeleted},
		{Path: "CLAUDE.md", Status: provider.PathRenamed, PreviousPath: "docs/CLAUDE.md"},
		{Path: "copy.go", Status: provider.PathCopied, PreviousPath: "src.go"},
		{Path: "mode.sh", Status: provider.PathModified},
		{Path: "odd.go", Status: provider.PathModified},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %+v; want %+v", got, want)
	}
}

func filesPage(page int) string {
	files := make([]string, 0, changePageSize)
	for i := 0; i < changePageSize; i++ {
		files = append(files, fmt.Sprintf(`{"filename":"f%d-%d.go","status":"added"}`, page, i))
	}
	return "[" + strings.Join(files, ",") + "]"
}

func TestChangedPaths_StopsAtTheCapAndSaysSo(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		fmt.Fprint(w, filesPage(page))
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).ChangedPaths(context.Background(), "webgrip/ploeg", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != provider.MaxChangedPaths || !got.Truncated || pages != provider.MaxChangedPaths/changePageSize+1 {
		t.Errorf("paths %d truncated %v after %d pages; want %d, true and one page past the cap",
			len(got.Paths), got.Truncated, pages, provider.MaxChangedPaths)
	}
}

func TestChangedPaths_ExactlyTheCapIsNotTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page > provider.MaxChangedPaths/changePageSize {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, filesPage(page))
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).ChangedPaths(context.Background(), "webgrip/ploeg", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != provider.MaxChangedPaths || got.Truncated {
		t.Errorf("paths %d truncated %v; want %d, false", len(got.Paths), got.Truncated, provider.MaxChangedPaths)
	}
}

func TestChangedPaths_FailsOnAForgeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := (&Provider{BaseURL: srv.URL}).ChangedPaths(context.Background(), "webgrip/ploeg", 9); err == nil {
		t.Fatal("a forge error must fail the read, never report an empty list")
	}
	if _, err := (&Provider{BaseURL: srv.URL}).ChangedPaths(context.Background(), "ploeg", 9); err == nil {
		t.Fatal("a repo without an owner must fail")
	}
}
