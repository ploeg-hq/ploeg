package trackerbinding

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func testRegistry() *Registry {
	return &Registry{
		Config: &config.File{Teams: map[string]config.Team{"silver": {}}},
		Static: target.Table{Targets: map[string]work.Target{"app": {Owner: "acme", Repo: "app"}}},
		Live:   target.NewLive(nil),
		Log:    slog.New(slog.DiscardHandler),
	}
}

func binding(id int64, scope, state string) store.TrackerBinding {
	return store.TrackerBinding{ID: id, Team: "silver", Connection: "clickup", ScopeID: scope, State: state,
		TrackerBindingFields: store.TrackerBindingFields{Default: "app"}}
}

func routes(r *Registry, scope string) bool {
	_, ok, _ := r.Live.Route(target.Request{Scope: scope, Team: "silver", LabelsRead: true})
	return ok
}

func TestOnlyActiveAndAwaitingWebhookBindingsRoute(t *testing.T) {
	r := testRegistry()
	r.install(1, []store.TrackerBinding{
		binding(1, "10", store.BindingActive),
		binding(2, "20", store.BindingAwaitingWebhook),
		binding(3, "30", store.BindingShadowed),
		binding(4, "40", store.BindingInvalid),
	})
	for scope, want := range map[string]bool{"10": true, "20": true, "30": false, "40": false} {
		if got := routes(r, scope); got != want {
			t.Errorf("scope %s routes = %v, want %v", scope, got, want)
		}
	}
	if _, pinned := r.ScopeTeam("30"); pinned {
		t.Error("a shadowed binding pins its Scope")
	}
}

func TestAnOlderRebuildNeverReplacesANewerTable(t *testing.T) {
	r := testRegistry()
	r.install(5, []store.TrackerBinding{binding(1, "10", store.BindingActive)})
	r.install(3, []store.TrackerBinding{binding(2, "20", store.BindingActive)})
	if !routes(r, "10") || routes(r, "20") {
		t.Fatal("a rebuild that read revision 3 replaced the table built from revision 5")
	}
	if got := r.Bindings(); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("bindings = %+v", got)
	}
}

type countingStore struct {
	entered chan struct{}
	release chan struct{}
}

func (c *countingStore) LiveTrackerBindings(context.Context) ([]store.TrackerBinding, error) {
	c.entered <- struct{}{}
	<-c.release
	return nil, nil
}
func (c *countingStore) TrackerBindingRevision(context.Context) (int64, error) { return 1, nil }
func (c *countingStore) SetTrackerBindingState(context.Context, int64, string, store.BindingStateChange, string) (bool, error) {
	return false, nil
}
func (c *countingStore) SetTrackerBindingWebhook(context.Context, int64, string) error { return nil }
func (c *countingStore) TrackerBindingWebhookUsers(context.Context, string, string, int64) (int, error) {
	return 0, nil
}
func (c *countingStore) RegisteredTrackerBindingWebhook(context.Context, string, string) (string, error) {
	return "", nil
}

func TestRebuildsRunOneAtATime(t *testing.T) {
	st := &countingStore{entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	r := testRegistry()
	r.Store = st
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_ = r.Rebuild(context.Background())
			done <- struct{}{}
		}()
	}
	<-st.entered
	select {
	case <-st.entered:
		t.Error("a second rebuild read the bindings while the first was still running")
	case <-time.After(200 * time.Millisecond):
	}
	st.release <- struct{}{}
	st.release <- struct{}{}
	<-done
	<-done
}
