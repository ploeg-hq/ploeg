package knowledgegraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

var millisecondDateTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{1,3})?Z$`)

// refusingServer answers like Omnigraph 0.14: a DateTime literal that names
// more than milliseconds is a 400.
func refusingServer(t *testing.T) (*omnigraph.Client, *map[string]any) {
	t.Helper()
	params := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body: %v", err)
		}
		params = body.Params
		for _, name := range []string{"written_at", "observed_at", "expires_at", "accepted_at"} {
			if at, ok := params[name].(string); ok && !millisecondDateTime.MatchString(at) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"param '` + name + `': invalid DateTime literal '` + at + `'"}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"commit":{"graph_commit_id":"c2"}}`))
	}))
	t.Cleanup(srv.Close)
	return &omnigraph.Client{BaseURL: srv.URL, Token: "t"}, &params
}

// Postgres keeps microseconds and an Omnigraph DateTime milliseconds, so a
// note with a Postgres time was refused on every sweep.
func TestNotesWrite_SendsMillisecondDateTimes(t *testing.T) {
	client, params := refusingServer(t)
	at := time.Date(2026, 11, 24, 22, 9, 52, 279_300_000, time.UTC)
	note := store.PendingNote{Key: "ploeg-1/0", WorkItemID: 11, RootWorkItemID: 11, Repository: "gitlab/acme/shop", WrittenBy: "run:ploeg-1",
		CreatedAt: at.Add(1234 * time.Nanosecond), ObservedAt: at, ExpiresAt: at.Add(time.Hour + 5*time.Microsecond)}.Note()
	note.Type, note.Title, note.Body = "Pitfall", "Times", "They carry microseconds."
	note.Level, note.AuthorKind, note.Author, note.Trust = notes.LevelWorkItem, notes.AuthorRun, "ploeg-1", notes.Untrusted
	if _, err := (&Notes{Client: client, Graph: "notes"}).Write(context.Background(), note, notes.Decision{}, "c1"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"observed_at": "2026-11-24T22:09:52.279Z", "written_at": "2026-11-24T22:09:52.279Z", "expires_at": "2026-11-24T23:09:52.279Z"} {
		if got := (*params)[name]; got != want {
			t.Errorf("%s %v, want %s", name, got, want)
		}
	}
}

func TestPutConcept_SendsAMillisecondDateTime(t *testing.T) {
	client, params := refusingServer(t)
	p := store.ConceptPromotion{TargetLevel: notes.LevelTenant, ApprovedBy: "ada", DecidedAt: time.Date(2026, 11, 24, 22, 9, 52, 602_287_000, time.UTC)}
	p.Note.Key, p.Note.Type, p.Note.Title, p.Note.Body = "ploeg-1/0", "Pitfall", "Times", "They carry microseconds."
	if _, err := (&Knowledge{Client: client, Review: client, Graph: "knowledge"}).PutConcept(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got := (*params)["accepted_at"]; got != "2026-11-24T22:09:52.602Z" {
		t.Errorf("accepted_at %v", got)
	}
}
