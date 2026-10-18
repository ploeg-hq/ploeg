package githubissues

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestDependenciesReadTheIssuesBlockingAnIssue(t *testing.T) {
	var gotPath, gotQuery string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprintf(w, `[
			{"number":3,"title":"Schema first","state":"open","repository_url":"%[1]s/repos/webgrip/ploeg-test"},
			{"number":9,"title":"Elsewhere","state":"closed","repository_url":"%[1]s/repos/Other/Repo"},
			{"number":12,"title":"Self","state":"open","repository_url":"%[1]s/repos/webgrip/ploeg-test"},
			{"number":0}]`, srv.URL)
	}))
	defer srv.Close()
	p := testProvider()
	p.APIURL = srv.URL
	p.Tokens = &staticTokens{tokens: []string{"t"}}
	got, err := p.Dependencies(context.Background(), "webgrip:ploeg-test:12")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/webgrip/ploeg-test/issues/12/dependencies/blocked_by" || gotQuery != "per_page=50" {
		t.Errorf("request = %s?%s", gotPath, gotQuery)
	}
	want := []provider.TrackerDependency{
		{ExternalID: "webgrip:ploeg-test:3", Title: "Schema first", Kind: provider.DependencyBlockedBy},
		{ExternalID: "other:repo:9", Title: "Elsewhere", Kind: provider.DependencyBlockedBy, Done: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %+v; want %+v", got, want)
	}
}

func TestDependenciesNeedCredentials(t *testing.T) {
	if _, err := testProvider().Dependencies(context.Background(), "webgrip:ploeg-test:12"); err == nil {
		t.Fatal("a provider without credentials read dependencies")
	}
	var _ provider.DependencyReader = (*Provider)(nil)
}
