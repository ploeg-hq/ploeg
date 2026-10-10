package clickup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestParentsReadsTheParentAndItsName(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/task/abc":
			fmt.Fprint(w, `{"id":"abc","name":"child","parent":"epic1"}`)
		case "/task/epic1":
			fmt.Fprint(w, `{"id":"epic1","name":"Billing"}`)
		case "/task/solo":
			fmt.Fprint(w, `{"id":"solo","name":"alone","parent":null}`)
		case "/task/orphan":
			fmt.Fprint(w, `{"id":"orphan","parent":"gone"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_x"}
	got, err := p.Parents(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := []provider.TrackerParent{{ExternalID: "epic1", Title: "Billing"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parents = %+v; want %+v", got, want)
	}
	if auth[0] != "pk_x" {
		t.Errorf("Authorization = %q; ClickUp takes the raw token", auth[0])
	}
	if got, err := p.Parents(context.Background(), "solo"); err != nil || len(got) != 0 {
		t.Fatalf("a task without a parent = %+v, %v", got, err)
	}
	if got, err := p.Parents(context.Background(), "orphan"); err != nil || !reflect.DeepEqual(got, []provider.TrackerParent{{ExternalID: "gone"}}) {
		t.Fatalf("a parent whose name cannot be read = %+v, %v", got, err)
	}
}
