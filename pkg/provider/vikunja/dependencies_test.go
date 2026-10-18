package vikunja

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestDependenciesReadBlockedAndFollowsRelationsWithTheirDoneFlag(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"id":42,"related_tasks":{
			"blocked":[{"id":7,"title":"Schema first","done":false},{"id":0},{"id":42}],
			"follows":[{"id":8,"title":"Spike","done":true},{"id":7,"title":"Schema first"}],
			"blocking":[{"id":99,"title":"waits for us"}],"precedes":[{"id":98}],"parenttask":[{"id":5}]}}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).Dependencies(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/tasks/42" {
		t.Errorf("path = %s", gotPath)
	}
	want := []provider.TrackerDependency{
		{ExternalID: "7", Title: "Schema first", Kind: provider.DependencyBlockedBy},
		{ExternalID: "8", Title: "Spike", Kind: provider.DependencyFollows, Done: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %+v; want %+v", got, want)
	}
}

func TestDependenciesOfATaskWithoutRelationsIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":42,"related_tasks":null}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).Dependencies(context.Background(), "42")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("dependencies = %#v, %v; want an empty list", got, err)
	}
	if _, err := (&Provider{}).Dependencies(context.Background(), "42"); err == nil {
		t.Fatal("an unconfigured provider read relations")
	}
}

func TestProviderReadsDependencies(t *testing.T) {
	var _ provider.DependencyReader = (*Provider)(nil)
}
