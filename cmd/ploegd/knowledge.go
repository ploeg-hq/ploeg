package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/knowledgegraph"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
)

// notesGraph wires the Tenant's notes graph (ADR-0088, proposed). It is off
// unless the server, the graph and the run actor's token are all set; a
// partial setting is an error so a typo cannot silently turn notes off.
func notesGraph(log *slog.Logger) (*knowledgegraph.Notes, error) {
	url := os.Getenv("PLOEG_OMNIGRAPH_URL")
	graph := os.Getenv("PLOEG_NOTES_GRAPH")
	token := os.Getenv("PLOEG_OMNIGRAPH_RUN_TOKEN")
	if url == "" && graph == "" && token == "" {
		return nil, nil
	}
	if url == "" || graph == "" || token == "" {
		return nil, errors.New("work item notes need PLOEG_OMNIGRAPH_URL, PLOEG_NOTES_GRAPH and PLOEG_OMNIGRAPH_RUN_TOKEN together")
	}
	log.Info("work item notes", "enabled", true, "graph", graph)
	return &knowledgegraph.Notes{
		Client: &omnigraph.Client{BaseURL: url, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}},
		Graph:  graph,
	}, nil
}
