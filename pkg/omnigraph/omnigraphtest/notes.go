// Package omnigraphtest fakes an Omnigraph server's notes graph over HTTP,
// answering the tenant-notes stored queries the way Omnigraph 0.11 does,
// including the conditional route that refuses a write with 412 once the
// branch moved past the commit it names.
package omnigraphtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Notes is an in-memory notes graph behind an httptest server.
type Notes struct {
	URL   string
	Token string
	Graph string

	mu     sync.Mutex
	notes  map[string]map[string]any
	edges  map[[2]string]bool
	commit int
	// Calls counts invocations per stored query.
	Calls map[string]int
	// FailWrites makes the next n writes answer 503 without writing.
	FailWrites int
	// LoseWriteAnswers makes the next n writes write and then answer 503,
	// as when a response is lost after the commit.
	LoseWriteAnswers int
	// RaceWrites publishes an unrelated commit just before each of the next
	// n conditional writes, as another writer would, so they answer 412.
	RaceWrites int
	// Legacy serves only the queries of the bundle before ADR-0122: no
	// repository or subject reads and no conditional route.
	Legacy bool
	// Down makes every call answer 503.
	Down bool
}

// NewNotes starts a fake notes graph and stops it when t ends.
func NewNotes(t *testing.T) *Notes {
	t.Helper()
	n := &Notes{Token: "graph-run-token-secret", Graph: "acme-notes", notes: map[string]map[string]any{},
		edges: map[[2]string]bool{}, Calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(n.serve))
	t.Cleanup(srv.Close)
	n.URL = srv.URL
	return n
}

// Keys lists the keys the graph holds.
func (n *Notes) Keys() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	keys := make([]string, 0, len(n.notes))
	for k := range n.notes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Note returns the stored row for key.
func (n *Notes) Note(key string) map[string]any {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.notes[key]
}

// Supersedes reports whether the graph holds the Supersedes edge from newer
// to older.
func (n *Notes) Supersedes(newer, older string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.edges[[2]string{newer, older}]
}

// Put stores one row as a write by another ploegd would, and publishes a
// commit.
func (n *Notes) Put(row map[string]any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notes[fmt.Sprint(row["key"])] = row
	n.commit++
}

var rootNoteColumns = []string{"key", "work_item", "repository", "client", "type", "title", "description", "resource", "body",
	"tags", "suggested_scope", "written_by", "written_at", "level", "subject", "author_kind", "author", "source_refs",
	"trust", "observed_at", "expires_at", "supersedes", "contradicts", "promoted_from", "evidence", "approved_by"}

var legacyRootNoteColumns = []string{"key", "work_item", "repository", "type", "title", "description", "resource", "body",
	"tags", "suggested_scope", "written_by", "written_at"}

func (n *Notes) serve(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	prefix := "/graphs/" + n.Graph + "/queries/"
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+n.Token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	name, conditional := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/if-graph-commit")
	ifCommit := r.Header.Get("Omnigraph-If-Graph-Commit")
	n.Calls[name]++
	if n.Down {
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	if conditional != (ifCommit != "") {
		http.Error(w, `{"error":"Omnigraph-If-Graph-Commit requires the fail-closed conditional route","code":"bad_request"}`, http.StatusBadRequest)
		return
	}
	legacyOnly := map[string]bool{"repository_notes": true, "subject_notes": true, "client_note_count": true, "delete_client_notes": true, "add_note_superseding": true}
	if n.Legacy && (conditional || legacyOnly[name]) {
		http.Error(w, `{"error":"stored query not found","code":"not_found"}`, http.StatusNotFound)
		return
	}
	var body struct {
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch name {
	case "add_note", "add_note_superseding":
		if conditional {
			if n.RaceWrites > 0 {
				n.RaceWrites--
				n.commit++
			}
			if ifCommit != n.head() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPreconditionFailed)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "precondition failed", "precondition_failure": map[string]any{"expected": ifCommit, "actual": n.head()}})
				return
			}
		}
		if n.FailWrites > 0 {
			n.FailWrites--
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		row := map[string]any{}
		for k, v := range body.Params {
			if v != nil {
				row[k] = v
			}
		}
		row["root_work_item"] = row["root"]
		delete(row, "root")
		key, _ := row["key"].(string)
		n.notes[key] = row
		if name == "add_note_superseding" {
			n.edges[[2]string{key, fmt.Sprint(row["supersedes"])}] = true
		}
		n.commit++
		if n.LoseWriteAnswers > 0 {
			n.LoseWriteAnswers--
			http.Error(w, `{"error":"interrupted"}`, http.StatusServiceUnavailable)
			return
		}
		n.reply(w, map[string]any{"query_name": name, "affected_nodes": 1, "commit": map[string]any{"graph_commit_id": n.head()}})
	case "root_notes":
		columns := rootNoteColumns
		if n.Legacy {
			columns = legacyRootNoteColumns
		}
		n.rows(w, func(row map[string]any) bool { return row["root_work_item"] == body.Params["root"] }, columns)
	case "repository_notes":
		n.rows(w, func(row map[string]any) bool {
			return row["repository"] == body.Params["repository"] && row["level"] == "repository"
		}, append(append([]string(nil), rootNoteColumns...), "root_work_item"))
	case "subject_notes":
		n.rows(w, func(row map[string]any) bool { return row["subject"] == body.Params["subject"] },
			append(append([]string(nil), rootNoteColumns...), "root_work_item"))
	case "note_by_key":
		var rows []map[string]any
		if row, ok := n.notes[fmt.Sprint(body.Params["key"])]; ok {
			rows = append(rows, map[string]any{"key": row["key"], "root_work_item": row["root_work_item"], "written_at": row["written_at"]})
		}
		n.reply(w, map[string]any{"rows": rows, "graph_commit_id": n.head()})
	case "client_note_count":
		count := 0
		for _, row := range n.notes {
			if row["client"] == body.Params["client"] {
				count++
			}
		}
		n.reply(w, map[string]any{"rows": []map[string]any{{"notes": count}}, "graph_commit_id": n.head()})
	case "delete_client_notes":
		deleted := 0
		for k, row := range n.notes {
			if row["client"] == body.Params["client"] {
				delete(n.notes, k)
				for e := range n.edges {
					if e[0] == k || e[1] == k {
						delete(n.edges, e)
					}
				}
				deleted++
			}
		}
		if deleted == 0 {
			n.reply(w, map[string]any{"query_name": name, "affected_nodes": 0, "commit": nil})
			return
		}
		n.commit++
		n.reply(w, map[string]any{"query_name": name, "affected_nodes": deleted, "commit": map[string]any{"graph_commit_id": n.head()}})
	default:
		http.NotFound(w, r)
	}
}

func (n *Notes) rows(w http.ResponseWriter, match func(map[string]any) bool, columns []string) {
	var rows []map[string]any
	for _, row := range n.notes {
		if !match(row) {
			continue
		}
		out := map[string]any{}
		for _, c := range columns {
			if v, ok := row[c]; ok {
				out[c] = v
			}
		}
		rows = append(rows, out)
	}
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["written_at"]) > fmt.Sprint(rows[j]["written_at"]) })
	n.reply(w, map[string]any{"rows": rows, "graph_commit_id": n.head()})
}

func (n *Notes) head() string { return fmt.Sprintf("commit-%d", n.commit) }

func (n *Notes) reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
