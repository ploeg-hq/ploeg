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

// Knowledge is an in-memory reviewed knowledge graph behind an httptest
// server, answering the tenant-knowledge read queries.
type Knowledge struct {
	URL   string
	Token string
	Graph string

	mu       sync.Mutex
	concepts map[string]map[string]any
	commit   int
	// Down makes every call answer 503.
	Down bool
	// BumpBetweenReads publishes a commit after the next n reads, as a write
	// landing between the reads of one request would.
	BumpBetweenReads int
}

// NewKnowledge starts a fake knowledge graph and stops it when t ends.
func NewKnowledge(t *testing.T) *Knowledge {
	t.Helper()
	k := &Knowledge{Token: "graph-run-token-secret", Graph: "acme-knowledge", concepts: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(k.serve))
	t.Cleanup(srv.Close)
	k.URL = srv.URL
	return k
}

// Put stores one accepted concept as put_concept would.
func (k *Knowledge) Put(slug, scope, client, repository, document string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	row := map[string]any{"slug": slug, "scope": scope, "body": document, "type": "Fact", "title": slug}
	if client != "" {
		row["client"] = client
	}
	if repository != "" {
		row["repository"] = repository
	}
	k.concepts[slug] = row
	k.commit++
}

func (k *Knowledge) serve(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	prefix := "/graphs/" + k.Graph + "/queries/"
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+k.Token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if k.Down {
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
	match := map[string]func(map[string]any) bool{
		"tenant_concepts": func(c map[string]any) bool { return c["scope"] == "tenant" },
		"client_concepts": func(c map[string]any) bool {
			return c["scope"] == "client" && c["client"] == body.Params["client"]
		},
		"repository_concepts": func(c map[string]any) bool {
			return c["scope"] == "repository" && c["repository"] == body.Params["repository"]
		},
	}[strings.TrimPrefix(r.URL.Path, prefix)]
	if match == nil {
		http.NotFound(w, r)
		return
	}
	rows := []map[string]any{}
	for _, c := range k.concepts {
		if match(c) {
			rows = append(rows, c)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["slug"]) < fmt.Sprint(rows[j]["slug"]) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"rows": rows, "graph_commit_id": fmt.Sprintf("commit-%d", k.commit)})
	if k.BumpBetweenReads > 0 {
		k.BumpBetweenReads--
		k.commit++
	}
}
