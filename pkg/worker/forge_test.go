package worker

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func forgeStub(t *testing.T, body string) (baseURL string, requestURI *string, header *http.Header) {
	t.Helper()
	var gotURI string
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &gotURI, &gotHeader
}

func TestFindOpenChangeRequestForgejo(t *testing.T) {
	body := `[{"html_url":"https://forge/x/y/pulls/7",
	           "head":{"ref":"ploeg/VIK-1"},"base":{"ref":"main"}}]`
	base, uri, hdr := forgeStub(t, body)

	ref := harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y", BaseBranch: "main"}
	got, err := findOpenChangeRequestURL(ref, "tok", "ploeg/VIK-1")
	if err != nil {
		t.Fatalf("findOpenChangeRequest: %v", err)
	}
	if want := "https://forge/x/y/pulls/7"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if !strings.HasPrefix(*uri, "/api/v1/repos/x/y/pulls") {
		t.Errorf("uri = %q, want the Forgejo pulls endpoint", *uri)
	}
	if h := hdr.Get("Authorization"); h != "token tok" {
		t.Errorf("Authorization = %q, want %q", h, "token tok")
	}
}

func TestFindOpenChangeRequestUnsetDialectIsForgejo(t *testing.T) {
	base, uri, _ := forgeStub(t, `[]`)
	ref := harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y"}
	if _, err := findOpenChangeRequestURL(ref, "tok", "b"); err != nil {
		t.Fatalf("findOpenChangeRequest: %v", err)
	}
	if !strings.Contains(*uri, "/api/v1/repos/") {
		t.Errorf("uri = %q, want the Forgejo endpoint for an unset dialect", *uri)
	}
}

func TestFindOpenChangeRequestGitLab(t *testing.T) {
	body := `[{"web_url":"https://gl/g/p/-/merge_requests/3",
	           "source_branch":"ploeg/VIK-1","target_branch":"main"}]`
	base, uri, hdr := forgeStub(t, body)

	ref := harness.RepoRef{
		Forge: harness.ForgeGitLab, ForgeURL: base,
		Owner: "g", Name: "p", BaseBranch: "main",
	}
	got, err := findOpenChangeRequestURL(ref, "tok", "ploeg/VIK-1")
	if err != nil {
		t.Fatalf("findOpenChangeRequest: %v", err)
	}
	if want := "https://gl/g/p/-/merge_requests/3"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if !strings.Contains(*uri, "source_branch=ploeg%2FVIK-1") {
		t.Errorf("uri = %q, want the branch filtered server-side and escaped", *uri)
	}
	if h := hdr.Get("PRIVATE-TOKEN"); h != "tok" {
		t.Errorf("PRIVATE-TOKEN = %q, want %q", h, "tok")
	}
	if hdr.Get("Authorization") != "" {
		t.Error("Authorization must not be sent to GitLab")
	}
}

func TestFindOpenChangeRequestGitLabEncodesSubgroupPath(t *testing.T) {
	base, uri, _ := forgeStub(t, `[]`)
	ref := harness.RepoRef{
		Forge: harness.ForgeGitLab, ForgeURL: base,
		Owner: "acme", Name: "internal/widgets",
	}
	if _, err := findOpenChangeRequestURL(ref, "tok", "b"); err != nil {
		t.Fatalf("findOpenChangeRequest: %v", err)
	}
	want := "/api/v4/projects/acme%2Finternal%2Fwidgets/merge_requests"
	if !strings.HasPrefix(*uri, want) {
		t.Errorf("uri = %q, want prefix %q", *uri, want)
	}
}

func TestFindOpenChangeRequestRejectsWrongBase(t *testing.T) {
	for _, c := range []struct{ name, body, forge string }{
		{"forgejo", `[{"html_url":"u","head":{"ref":"b"},"base":{"ref":"other"}}]`, harness.ForgeForgejo},
		{"gitlab", `[{"web_url":"u","source_branch":"b","target_branch":"other"}]`, harness.ForgeGitLab},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _, _ := forgeStub(t, c.body)
			ref := harness.RepoRef{Forge: c.forge, ForgeURL: base, Owner: "o", Name: "r", BaseBranch: "main"}
			got, err := findOpenChangeRequestURL(ref, "tok", "b")
			if err != nil {
				t.Fatalf("findOpenChangeRequest: %v", err)
			}
			if got != "" {
				t.Errorf("url = %q, want empty for a mismatched base", got)
			}
		})
	}
}

func TestFindOpenChangeRequestUnsupportedForge(t *testing.T) {
	ref := harness.RepoRef{Forge: "bitbucket", ForgeURL: "http://unused", Owner: "o", Name: "r"}
	_, err := findOpenChangeRequestURL(ref, "tok", "b")
	if !errors.Is(err, errUnsupportedForge) {
		t.Fatalf("err = %v, want errUnsupportedForge", err)
	}
	if !strings.Contains(err.Error(), "bitbucket") {
		t.Errorf("err = %v, want it to name the dialect", err)
	}
}

func TestFindOpenChangeRequestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	ref := harness.RepoRef{Forge: harness.ForgeGitLab, ForgeURL: srv.URL, Owner: "o", Name: "r"}
	if _, err := findOpenChangeRequestURL(ref, "tok", "b"); err == nil {
		t.Fatal("want an error for HTTP 401, got nil")
	}
}
