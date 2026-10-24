package omnigraph

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A conditional write goes to the fail-closed route with the commit in its
// header, and a moved branch is ErrPreconditionFailed, not a retryable error.
func TestMutateIf_UsesTheConditionalRouteAndReportsAMovedBranch(t *testing.T) {
	var path, header string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, header = r.URL.Path, r.Header.Get("Omnigraph-If-Graph-Commit")
		_, _ = io.Copy(io.Discard, r.Body)
		if header != "c1" {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(`{"error":"precondition failed","precondition_failure":{"expected":"c0","actual":"c1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"commit":{"graph_commit_id":"c2"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t"}
	commit, err := c.MutateIf(context.Background(), "notes", "add_note", nil, "c1")
	if err != nil || commit != "c2" || path != "/graphs/notes/queries/add_note/if-graph-commit" {
		t.Fatalf("conditional write: %q %v at %s", commit, err, path)
	}
	_, err = c.MutateIf(context.Background(), "notes", "add_note", nil, "c0")
	if !errors.Is(err, ErrPreconditionFailed) || Retryable(err) {
		t.Errorf("a moved branch answered %v", err)
	}
	if _, err := c.MutateIf(context.Background(), "notes", "add_note", nil, ""); err == nil {
		t.Error("a conditional write without a commit was sent")
	}
	if _, err := c.Mutate(context.Background(), "notes", "add_note", nil); header != "" || path != "/graphs/notes/queries/add_note" || err == nil {
		t.Errorf("an unconditional write carried %q to %s", header, path)
	}
}

// Every request declares the HTTP contract, which an Omnigraph 0.14 server
// requires before it reads a single field.
func TestInvoke_DeclaresTheHTTPContract(t *testing.T) {
	var contracts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contracts = append(contracts, r.Header.Get("Omnigraph-Http-Api"))
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"rows":[],"graph_commit_id":"c1","commit":{"graph_commit_id":"c2"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t"}
	if _, err := c.Read(context.Background(), "notes", "notes_for_task", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Mutate(context.Background(), "notes", "add_note", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MutateIf(context.Background(), "notes", "add_note", nil, "c1"); err != nil {
		t.Fatal(err)
	}
	for i, contract := range contracts {
		if contract != "0.14" {
			t.Errorf("request %d declared contract %q", i, contract)
		}
	}
	if len(contracts) != 3 {
		t.Errorf("saw %d requests", len(contracts))
	}
}
