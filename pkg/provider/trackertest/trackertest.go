// Package trackertest is the conformance suite every tracker adapter passes
// (ADR-0183). An adapter's tests supply a Fixture: the adapter itself, a
// fake of its vendor API over a vendor-neutral World, and a signer for its
// webhooks. The suite then checks the behaviour the core relies on, the same
// way for every adapter. A capability's cases run only when the adapter
// declares it.
package trackertest

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Task is one tracker item in the World, in no vendor's shape.
type Task struct {
	ID    string
	Title string
	// Body is the item's description as Markdown.
	Body string
	// Scope is the container id the adapter must report as ExternalScope.
	Scope string
	// Closed is the tracker's own "done or closed".
	Closed bool
	// Status is the item's current status name, where the tracker has one.
	Status string
	// DependsOn names the items this one waits for.
	DependsOn []string
	// Comments holds each comment as the text a person reads: the fake
	// renders what the adapter sent the way the vendor would show it.
	Comments []string
	// Fields maps a dropdown field's name to its selected option name, for
	// an adapter that reads a Team from a field (ADR-0187).
	Fields map[string]string
}

// World is the tracker state a fake vendor API serves and records into.
type World struct {
	mu    sync.Mutex
	tasks map[string]*Task
}

// NewWorld returns a World holding tasks.
func NewWorld(tasks ...Task) *World {
	w := &World{tasks: map[string]*Task{}}
	for _, t := range tasks {
		t := t
		w.tasks[t.ID] = &t
	}
	return w
}

// Read returns a copy of the task and whether it exists.
func (w *World) Read(id string) (Task, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.tasks[id]
	if !ok {
		return Task{}, false
	}
	out := *t
	out.DependsOn = slices.Clone(t.DependsOn)
	out.Comments = slices.Clone(t.Comments)
	out.Fields = maps.Clone(t.Fields)
	return out, true
}

// Update changes a task under the World's lock. It reports false when the
// task does not exist.
func (w *World) Update(id string, change func(*Task)) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.tasks[id]
	if ok {
		change(t)
	}
	return ok
}

// Fixture adapts one tracker adapter to the suite.
type Fixture struct {
	// New returns the adapter pointed at baseURL, verifying webhooks with
	// secret (empty = no secret configured). configured false means no API
	// credential, so reads fail and write-backs are no-ops. It must route
	// the assignee "builder" to the Team "silver" and default to "default".
	// statuses is the operator's status mapping, for an adapter that maps
	// statuses (StatusMapping true); others ignore it.
	New func(baseURL, secret string, configured bool, statuses map[work.State]string) provider.TrackerProvider
	// Server fakes the vendor API over w.
	Server func(w *World) http.Handler
	// Assignment returns the vendor's webhook request assigning the task
	// to assignee, signed with secret. Each call returns a fresh request
	// with the same body.
	Assignment func(secret, taskID, assignee string) *http.Request
	// StatusMapping says the adapter moves items to operator-named
	// statuses rather than to a fixed vocabulary of its own.
	StatusMapping bool
	// TeamField is the name of the field the suite seeds a Team into, for
	// an adapter that declares AgentIdentity; New must configure the
	// adapter to read it. Empty means "Team".
	TeamField string
}

const (
	secret = "conformance-secret"
	// outcome is a comment shaped like the ones the core writes.
	outcome = "Ploeg finished this item. Its pull request is ready for review.\n\n**Outcome:** done\n**Pull request:** https://forge.example/o/r/pulls/7\n\n<sub>Posted by Ploeg.</sub>"
)

func seed(teamField string) *World {
	return NewWorld(
		Task{ID: "t1", Title: "Fix the login", Body: "## Steps\n\n- reproduce **it**", Scope: "901", Status: "to do", DependsOn: []string{"t2", "t3"},
			Fields: map[string]string{teamField: "Silver"}},
		Task{ID: "t2", Title: "Schema", Scope: "901", Closed: true, Status: "complete"},
		Task{ID: "t3", Title: "API", Scope: "901", Status: "in progress"},
	)
}

// Run runs every case the adapter's declared capabilities call for.
func Run(t *testing.T, f Fixture) {
	t.Helper()
	teamField := f.TeamField
	if teamField == "" {
		teamField = "Team"
	}
	open := func(t *testing.T, configured bool, statuses map[work.State]string) (*World, provider.TrackerProvider) {
		t.Helper()
		w := seed(teamField)
		srv := httptest.NewServer(f.Server(w))
		t.Cleanup(srv.Close)
		return w, f.New(srv.URL, secret, configured, statuses)
	}

	t.Run("webhooks are verified before they are read", func(t *testing.T) {
		_, tp := open(t, true, nil)
		if _, err := tp.ParseWebhook(f.Assignment("not-the-secret", "t1", "builder")); err == nil {
			t.Error("a delivery signed with another secret was accepted")
		}
		if _, err := tp.ParseWebhook(f.Assignment("", "t1", "builder")); err == nil {
			t.Error("a delivery signed with an empty key was accepted")
		}
		unsecured := f.New("http://127.0.0.1:0", "", true, nil)
		if _, err := unsecured.ParseWebhook(f.Assignment(secret, "t1", "builder")); err == nil {
			t.Error("a provider without a webhook secret accepted a delivery")
		}
	})

	t.Run("an assignment routes to its Team", func(t *testing.T) {
		_, tp := open(t, true, nil)
		events, err := tp.ParseWebhook(f.Assignment(secret, "t1", "builder"))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Kind != provider.TrackerAssigned || events[0].ExternalID != "t1" || events[0].Team != "silver" {
			t.Fatalf("events = %+v, want one assignment of t1 to silver", events)
		}
	})

	t.Run("a read returns the item as the tracker holds it", func(t *testing.T) {
		_, tp := open(t, true, nil)
		item, err := tp.FetchItem(context.Background(), "t1")
		if err != nil {
			t.Fatal(err)
		}
		if item.Provider != tp.Name() || item.ExternalID != "t1" || item.Title != "Fix the login" || item.ExternalScope != "901" || item.Closed {
			t.Errorf("item = %+v", item)
		}
		if item.Description != "## Steps\n\n- reproduce **it**" {
			t.Errorf("description = %q, want the Markdown body", item.Description)
		}
		if _, ok := tp.(provider.TaskLinker); ok && item.URL == "" {
			t.Error("an adapter that links tasks returned an item without a link")
		}
		closed, err := tp.FetchItem(context.Background(), "t2")
		if err != nil || !closed.Closed {
			t.Errorf("a closed item read as %+v, %v", closed, err)
		}
	})

	t.Run("a comment reads without Markdown markers", func(t *testing.T) {
		w, tp := open(t, true, nil)
		if err := tp.Comment(context.Background(), "t1", outcome); err != nil {
			t.Fatal(err)
		}
		task, _ := w.Read("t1")
		if len(task.Comments) != 1 {
			t.Fatalf("comments = %q, want one", task.Comments)
		}
		got := task.Comments[0]
		for _, marker := range []string{"**", "<sub>"} {
			if strings.Contains(got, marker) {
				t.Errorf("a person reads %q in %q", marker, got)
			}
		}
		for _, words := range []string{"Outcome: done", "https://forge.example/o/r/pulls/7", "Posted by Ploeg."} {
			if !strings.Contains(got, words) {
				t.Errorf("comment %q lost %q", got, words)
			}
		}
	})

	if f.StatusMapping {
		t.Run("only mapped states move an item, to the mapped status", func(t *testing.T) {
			w, tp := open(t, true, map[work.State]string{work.StateAwaitingReview: "in review"})
			if err := tp.SetStatus(context.Background(), "t1", work.StateNeedsHuman); err != nil {
				t.Fatal(err)
			}
			if task, _ := w.Read("t1"); task.Status != "to do" {
				t.Fatalf("an unmapped state moved the item to %q", task.Status)
			}
			if err := tp.SetStatus(context.Background(), "t1", work.StateAwaitingReview); err != nil {
				t.Fatal(err)
			}
			if task, _ := w.Read("t1"); task.Status != "in review" {
				t.Fatalf("status = %q, want the mapped %q", task.Status, "in review")
			}
		})
	}

	t.Run("without credentials write-backs do nothing and reads fail", func(t *testing.T) {
		w, tp := open(t, false, map[work.State]string{work.StateDone: "complete", work.StateAwaitingReview: "in review"})
		if err := tp.Comment(context.Background(), "t1", outcome); err != nil {
			t.Errorf("Comment: %v", err)
		}
		if err := tp.SetStatus(context.Background(), "t1", work.StateAwaitingReview); err != nil {
			t.Errorf("SetStatus: %v", err)
		}
		if task, _ := w.Read("t1"); len(task.Comments) != 0 || task.Status != "to do" {
			t.Errorf("an unconfigured adapter changed the tracker: %+v", task)
		}
		if _, err := tp.FetchItem(context.Background(), "t1"); err == nil {
			t.Error("an unconfigured adapter read an item")
		}
	})

	t.Run("dependencies name what an item waits for", func(t *testing.T) {
		_, tp := open(t, true, nil)
		reader, ok := tp.(provider.DependencyReader)
		if !ok {
			t.Skip("the adapter declares no dependency_reader")
		}
		deps, err := reader.Dependencies(context.Background(), "t1")
		if err != nil {
			t.Fatal(err)
		}
		done := map[string]bool{}
		for _, d := range deps {
			done[d.ExternalID] = d.Done
		}
		if len(deps) != 2 || !done["t2"] || done["t3"] {
			t.Fatalf("deps = %+v, want t2 done and t3 not", deps)
		}
		none, err := reader.Dependencies(context.Background(), "t3")
		if err != nil || len(none) != 0 {
			t.Fatalf("an item waiting for nothing reported %+v, %v", none, err)
		}
	})

	t.Run("a redelivery carries the same delivery id", func(t *testing.T) {
		_, tp := open(t, true, nil)
		ident, ok := tp.(provider.DeliveryIdentifier)
		if !ok {
			t.Skip("the adapter declares no delivery_identifier")
		}
		first := f.Assignment(secret, "t1", "builder")
		if _, err := tp.ParseWebhook(first); err != nil {
			t.Fatal(err)
		}
		id := ident.DeliveryID(first)
		if id == "" || ident.DeliveryID(f.Assignment(secret, "t1", "builder")) != id {
			t.Fatalf("delivery ids differ or are empty: %q", id)
		}
	})

	t.Run("a link names a page only for a task", func(t *testing.T) {
		_, tp := open(t, true, nil)
		linker, ok := tp.(provider.TaskLinker)
		if !ok {
			t.Skip("the adapter declares no task_linker")
		}
		if linker.TaskURL("t1") == "" || linker.TaskURL("") != "" {
			t.Fatalf("TaskURL(t1) = %q, TaskURL(\"\") = %q", linker.TaskURL("t1"), linker.TaskURL(""))
		}
	})

	t.Run("a delegate field names the Team", func(t *testing.T) {
		_, tp := open(t, true, nil)
		identity, ok := tp.(provider.AgentIdentity)
		if !ok {
			t.Skip("the adapter declares no agent identity")
		}
		item, err := tp.FetchItem(context.Background(), "t1")
		if err != nil {
			t.Fatal(err)
		}
		team, source, err := identity.Delegate(context.Background(), item)
		if err != nil || team != "silver" || source == "" {
			t.Fatalf("Delegate(t1) = %q, %q, %v; want silver from a named source", team, source, err)
		}
		team, source, err = identity.Delegate(context.Background(), work.WorkItem{ExternalID: "t1"})
		if err != nil || team != "silver" || source == "" {
			t.Fatalf("Delegate of an unread t1 = %q, %q, %v; want the adapter to read the item", team, source, err)
		}
		team, source, err = identity.Delegate(context.Background(), work.WorkItem{ExternalID: "t3"})
		if err != nil || team != "" || source != "" {
			t.Fatalf("Delegate(t3) = %q, %q, %v; want no Team for an item without the field", team, source, err)
		}
		if named, ok := tp.(provider.DelegateField); ok && !strings.EqualFold(named.DelegateFieldName(), teamField) {
			t.Fatalf("DelegateFieldName() = %q, want the field the Team is read from, %q", named.DelegateFieldName(), teamField)
		}
	})

	t.Run("an activity reads as a person would without Markdown markers", func(t *testing.T) {
		w, tp := open(t, true, nil)
		identity, ok := tp.(provider.AgentIdentity)
		if !ok {
			t.Skip("the adapter declares no agent identity")
		}
		if err := identity.PostActivity(context.Background(), "t1", provider.SessionActivity{Kind: provider.SessionThought, Body: "Reading **the login** flow"}); err != nil {
			t.Fatal(err)
		}
		if err := identity.PostActivity(context.Background(), "t1", provider.SessionActivity{Kind: provider.SessionResponse, Body: "Starting: **builder** then **reviewer**."}); err != nil {
			t.Fatal(err)
		}
		task, _ := w.Read("t1")
		if len(task.Comments) != 2 {
			t.Fatalf("comments = %q, want two", task.Comments)
		}
		for _, got := range task.Comments {
			for _, marker := range []string{"**", "_", "<sub>"} {
				if strings.Contains(got, marker) {
					t.Errorf("a person reads %q in %q", marker, got)
				}
			}
		}
		if !strings.Contains(task.Comments[0], "Reading the login flow") || !strings.Contains(task.Comments[1], "Starting: builder then reviewer.") {
			t.Errorf("comments %q lost their words", task.Comments)
		}
	})

	t.Run("external URLs reach the item", func(t *testing.T) {
		w, tp := open(t, true, nil)
		identity, ok := tp.(provider.AgentIdentity)
		if !ok {
			t.Skip("the adapter declares no agent identity")
		}
		if err := identity.SetExternalURLs(context.Background(), "t1", []provider.ExternalURL{{Label: "Pull request", URL: "https://forge.example/o/r/pulls/7"}}); err != nil {
			t.Fatal(err)
		}
		task, _ := w.Read("t1")
		if len(task.Comments) != 1 || !strings.Contains(task.Comments[0], "https://forge.example/o/r/pulls/7") || !strings.Contains(task.Comments[0], "Pull request") {
			t.Fatalf("comments = %q, want one naming the pull request and its link", task.Comments)
		}
	})

	t.Run("without credentials the delegate names no Team and writes nothing", func(t *testing.T) {
		w, tp := open(t, false, nil)
		identity, ok := tp.(provider.AgentIdentity)
		if !ok {
			t.Skip("the adapter declares no agent identity")
		}
		team, source, err := identity.Delegate(context.Background(), work.WorkItem{ExternalID: "t1"})
		if err != nil || team != "" || source != "" {
			t.Fatalf("Delegate = %q, %q, %v; want nothing from an unconfigured adapter", team, source, err)
		}
		if err := identity.PostActivity(context.Background(), "t1", provider.SessionActivity{Kind: provider.SessionResponse, Body: "Starting."}); err != nil {
			t.Errorf("PostActivity: %v", err)
		}
		if err := identity.SetExternalURLs(context.Background(), "t1", []provider.ExternalURL{{Label: "Pull request", URL: "https://forge.example/o/r/pulls/7"}}); err != nil {
			t.Errorf("SetExternalURLs: %v", err)
		}
		if task, _ := w.Read("t1"); len(task.Comments) != 0 {
			t.Errorf("an unconfigured adapter wrote %q", task.Comments)
		}
	})
}
