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

// tenantGraphs wires the Tenant's notes graph (ADR-0088) and reviewed
// knowledge graph (ADR-0087), both proposed. Each is off unless named; a
// named graph needs the server and the run actor's token, and a server or
// token without any graph is an error, so a typo cannot silently turn
// knowledge off.
func tenantGraphs(log *slog.Logger) (*knowledgegraph.Notes, *knowledgegraph.Knowledge, error) {
	url := os.Getenv("PLOEG_OMNIGRAPH_URL")
	token := os.Getenv("PLOEG_OMNIGRAPH_RUN_TOKEN")
	notesGraph := os.Getenv("PLOEG_NOTES_GRAPH")
	knowledgeGraph := os.Getenv("PLOEG_KNOWLEDGE_GRAPH")
	if url == "" && token == "" && notesGraph == "" && knowledgeGraph == "" {
		return nil, nil, nil
	}
	if url == "" || token == "" || (notesGraph == "" && knowledgeGraph == "") {
		return nil, nil, errors.New("tenant knowledge needs PLOEG_OMNIGRAPH_URL, PLOEG_OMNIGRAPH_RUN_TOKEN and PLOEG_NOTES_GRAPH or PLOEG_KNOWLEDGE_GRAPH")
	}
	client := &omnigraph.Client{BaseURL: url, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
	var notes *knowledgegraph.Notes
	var knowledge *knowledgegraph.Knowledge
	if notesGraph != "" {
		notes = &knowledgegraph.Notes{Client: client, Graph: notesGraph}
	}
	if knowledgeGraph != "" {
		knowledge = &knowledgegraph.Knowledge{Client: client, Graph: knowledgeGraph}
	}
	log.Info("tenant knowledge", "notes_graph", notesGraph, "knowledge_graph", knowledgeGraph)
	return notes, knowledge, nil
}
