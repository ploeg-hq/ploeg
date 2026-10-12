// Package omnigraphtest fakes an Omnigraph server's notes graph over HTTP,
// answering the tenant-notes stored queries the way Omnigraph 0.11 does.
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
	commit int
	// Calls counts invocations per stored query.
	Calls map[string]int
	// FailWrites makes the next n add_note calls answer 503 without writing.
	FailWrites int
	// LoseWriteAnswers makes the next n add_note calls write and then answer
	// 503, as when a response is lost after the commit.
	LoseWriteAnswers int
	// Down makes every call answer 503.
	Down bool
}

// NewNotes starts a fake notes graph and stops it when t ends.
func NewNotes(t *testing.T) *Notes {
	t.Helper()
	n := &Notes{Token: "graph-run-token-secret", Graph: "acme-notes", notes: map[string]map[string]any{}, Calls: map[string]int{}}
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
	name := strings.TrimPrefix(r.URL.Path, prefix)
	n.Calls[name]++
	if n.Down {
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
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
	case "add_note":
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
		n.commit++
		if n.LoseWriteAnswers > 0 {
			n.LoseWriteAnswers--
			http.Error(w, `{"error":"interrupted"}`, http.StatusServiceUnavailable)
			return
		}
		n.reply(w, map[string]any{"query_name": name, "affected_nodes": 1, "commit": map[string]any{"graph_commit_id": n.head()}})
	case "root_notes":
		var rows []map[string]any
		for _, row := range n.notes {
			if row["root_work_item"] == body.Params["root"] {
				out := map[string]any{}
				for k, v := range row {
					if k != "root_work_item" && k != "client" {
						out[k] = v
					}
				}
				rows = append(rows, out)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["written_at"]) > fmt.Sprint(rows[j]["written_at"]) })
		n.reply(w, map[string]any{"rows": rows, "graph_commit_id": n.head()})
	case "note_by_key":
		var rows []map[string]any
		if row, ok := n.notes[fmt.Sprint(body.Params["key"])]; ok {
			rows = append(rows, map[string]any{"key": row["key"], "root_work_item": row["root_work_item"], "written_at": row["written_at"]})
		}
		n.reply(w, map[string]any{"rows": rows, "graph_commit_id": n.head()})
	default:
		http.NotFound(w, r)
	}
}

func (n *Notes) head() string { return fmt.Sprintf("commit-%d", n.commit) }

func (n *Notes) reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
