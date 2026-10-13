// Package knowledgegraph reads and writes a Tenant's knowledge graphs through
// the stored queries of the tenant-knowledge and tenant-notes schemas
// (ADR-0087, ADR-0088, both proposed). Only ploegd uses it: the Run's pod
// never holds a graph credential.
package knowledgegraph

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Notes is one Tenant's notes graph, reached as its run actor.
type Notes struct {
	Client *omnigraph.Client
	Graph  string
}

// RootNotes reads the notes of one root Work Item and the commit they were
// read at.
func (n *Notes) RootNotes(ctx context.Context, rootWorkItemID int64) ([]harness.WorkItemNote, string, error) {
	res, err := n.Client.Read(ctx, n.Graph, "root_notes", map[string]any{"root": strconv.FormatInt(rootWorkItemID, 10)})
	if err != nil {
		return nil, "", err
	}
	notes := make([]harness.WorkItemNote, 0, len(res.Rows))
	for _, row := range res.Rows {
		note := harness.WorkItemNote{
			Key:            str(row["key"]),
			WorkItem:       str(row["work_item"]),
			Repository:     str(row["repository"]),
			Type:           str(row["type"]),
			Title:          str(row["title"]),
			Description:    str(row["description"]),
			Resource:       str(row["resource"]),
			Tags:           strs(row["tags"]),
			Body:           str(row["body"]),
			SuggestedScope: str(row["suggested_scope"]),
			WrittenBy:      str(row["written_by"]),
			WrittenAt:      timestamp(row["written_at"]),
		}
		if note.Key == "" || note.Title == "" || note.Body == "" {
			continue
		}
		notes = append(notes, note)
	}
	return notes, res.Commit, nil
}

// Has reports whether the graph already holds the note with key: the
// read-back after a write whose answer was lost.
func (n *Notes) Has(ctx context.Context, key string) (bool, error) {
	res, err := n.Client.Read(ctx, n.Graph, "note_by_key", map[string]any{"key": key})
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// Write upserts one pending note by its key and returns the commit.
func (n *Notes) Write(ctx context.Context, p store.PendingNote) (string, error) {
	l := p.Learning
	if l.Type == "" || l.Title == "" || l.Body == "" {
		return "", fmt.Errorf("note %s lacks a type, title or body", p.Key)
	}
	return n.Client.Mutate(ctx, n.Graph, "add_note", map[string]any{
		"key":             p.Key,
		"root":            strconv.FormatInt(p.RootWorkItemID, 10),
		"work_item":       strconv.FormatInt(p.WorkItemID, 10),
		"repository":      p.Repository,
		"client":          nil,
		"type":            l.Type,
		"title":           l.Title,
		"description":     nilIfEmpty(l.Description),
		"resource":        nilIfEmpty(l.Resource),
		"body":            l.Body,
		"tags":            nilIfNoTags(l.Tags),
		"suggested_scope": p.SuggestedScope,
		"written_by":      p.WrittenBy,
		"written_at":      p.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strs(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func timestamp(v any) time.Time {
	s, _ := v.(string)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfNoTags(tags []string) any {
	if len(tags) == 0 {
		return nil
	}
	return tags
}
