// Package knowledgegraph reads and writes a Tenant's knowledge graphs through
// the stored queries of the tenant-knowledge and tenant-notes schemas
// (ADR-0087, ADR-0088, ADR-0122). Only ploegd uses it: the Run's pod never
// holds a graph credential. Which notes a Run may read, and what a new note
// does to the notes on its subject, is decided by package notes.
package knowledgegraph

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
)

// Notes is one Tenant's notes graph, reached as its run actor.
type Notes struct {
	Client *omnigraph.Client
	Graph  string
}

// RootNotes reads the notes of one root Work Item and the commit they were
// read at.
func (n *Notes) RootNotes(ctx context.Context, rootWorkItemID int64) ([]notes.Note, string, error) {
	root := strconv.FormatInt(rootWorkItemID, 10)
	out, commit, err := n.read(ctx, "root_notes", map[string]any{"root": root})
	for i := range out {
		out[i].RootWorkItem = root
	}
	return out, commit, err
}

// RepositoryNotes reads the repository-level notes of one repository, of
// every Client: notes.ForRun keeps those of the reading Run's Client.
func (n *Notes) RepositoryNotes(ctx context.Context, repository string) ([]notes.Note, string, error) {
	return n.read(ctx, "repository_notes", map[string]any{"repository": repository})
}

// SubjectNotes reads every note on one subject, the read a new note on that
// subject is decided on.
func (n *Notes) SubjectNotes(ctx context.Context, subject string) ([]notes.Note, string, error) {
	return n.read(ctx, "subject_notes", map[string]any{"subject": subject})
}

// ScopeNotes reads the notes a Run in scope may be briefed with, its root's
// and its repository's, at one graph commit: when a write lands between the
// two reads they are read again once. A graph whose bundle predates
// repository notes answers 404 for that read, and the root's notes stand
// alone. The rows are not filtered: pass them to notes.ForRun.
func (n *Notes) ScopeNotes(ctx context.Context, rootWorkItemID int64, repository string) ([]notes.Note, string, error) {
	var all []notes.Note
	var commit string
	for attempt := 0; attempt < 2; attempt++ {
		root, rootCommit, err := n.RootNotes(ctx, rootWorkItemID)
		if err != nil {
			return nil, "", err
		}
		all, commit = root, rootCommit
		if repository == "" {
			return all, commit, nil
		}
		repo, repoCommit, err := n.RepositoryNotes(ctx, repository)
		var status *omnigraph.StatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			return all, commit, nil
		}
		if err != nil {
			return nil, "", err
		}
		all = append(all, repo...)
		if repoCommit == rootCommit {
			break
		}
		commit = repoCommit
	}
	return all, commit, nil
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

// Write appends note as decided against the graph at basedOn, the commit of
// the SubjectNotes read the decision came from, and returns the new commit.
// A note that supersedes another also records the Supersedes edge in the
// same commit. omnigraph.ErrPreconditionFailed means the graph moved: read
// the subject again and decide again.
func (n *Notes) Write(ctx context.Context, note notes.Note, d notes.Decision, basedOn string) (string, error) {
	note.Supersedes = d.Supersedes
	note.Contradicts = d.Contradicts
	if err := note.Validate(); err != nil {
		return "", err
	}
	params := map[string]any{
		"key":             note.Key,
		"root":            note.RootWorkItem,
		"work_item":       note.WorkItem,
		"repository":      note.Repository,
		"client":          nilIfEmpty(note.Client),
		"type":            note.Type,
		"title":           note.Title,
		"description":     nilIfEmpty(note.Description),
		"resource":        nilIfEmpty(note.Resource),
		"body":            note.Body,
		"tags":            nilIfNone(note.Tags),
		"suggested_scope": suggested(note),
		"written_by":      note.WrittenBy(),
		"written_at":      note.WrittenAt.UTC().Format(time.RFC3339Nano),
		"level":           string(note.Level),
		"subject":         nilIfEmpty(note.Subject),
		"author_kind":     string(note.AuthorKind),
		"author":          note.Author,
		"source_refs":     nilIfNone(note.SourceRefs),
		"trust":           string(note.Trust),
		"observed_at":     note.ObservedAt.UTC().Format(time.RFC3339Nano),
		"expires_at":      nilIfZero(note.ExpiresAt),
		"contradicts":     nilIfNone(note.Contradicts),
		"promoted_from":   nilIfEmpty(note.PromotedFrom),
		"evidence":        nilIfNone(note.Evidence),
		"approved_by":     nilIfEmpty(note.ApprovedBy),
	}
	query := "add_note"
	if note.Supersedes != "" {
		query = "add_note_superseding"
		params["supersedes"] = note.Supersedes
	}
	return n.Client.MutateIf(ctx, n.Graph, query, params, basedOn)
}

// PurgeClient removes every note of client from the graph's main branch and
// reports how many remain, which is zero when the purge is done. Older
// commits keep the rows until an operator runs Omnigraph's offline cleanup
// (ADR-0122).
func (n *Notes) PurgeClient(ctx context.Context, client string) (int, error) {
	if client == "" {
		return 0, errors.New("a purge names one Client")
	}
	if _, err := n.Client.Mutate(ctx, n.Graph, "delete_client_notes", map[string]any{"client": client}); err != nil {
		return 0, err
	}
	res, err := n.Client.Read(ctx, n.Graph, "client_note_count", map[string]any{"client": client})
	if err != nil {
		return 0, err
	}
	return count(res, "notes"), nil
}

func (n *Notes) read(ctx context.Context, query string, params map[string]any) ([]notes.Note, string, error) {
	res, err := n.Client.Read(ctx, n.Graph, query, params)
	if err != nil {
		return nil, "", err
	}
	out := make([]notes.Note, 0, len(res.Rows))
	for _, row := range res.Rows {
		note := noteFromRow(row)
		if note.Key == "" || note.Title == "" || note.Body == "" {
			continue
		}
		out = append(out, note)
	}
	return out, res.Commit, nil
}

func noteFromRow(row map[string]any) notes.Note {
	return notes.Note{
		Key:            str(row["key"]),
		RootWorkItem:   str(row["root_work_item"]),
		WorkItem:       str(row["work_item"]),
		Repository:     str(row["repository"]),
		Client:         str(row["client"]),
		Type:           str(row["type"]),
		Title:          str(row["title"]),
		Description:    str(row["description"]),
		Resource:       str(row["resource"]),
		Tags:           strs(row["tags"]),
		Body:           str(row["body"]),
		SuggestedLevel: notes.Level(str(row["suggested_scope"])),
		Level:          notes.Level(str(row["level"])),
		Subject:        str(row["subject"]),
		AuthorKind:     notes.AuthorKind(str(row["author_kind"])),
		Author:         str(row["author"]),
		SourceRefs:     strs(row["source_refs"]),
		Trust:          notes.Trust(str(row["trust"])),
		ObservedAt:     timestamp(row["observed_at"]),
		WrittenAt:      timestamp(row["written_at"]),
		ExpiresAt:      timestamp(row["expires_at"]),
		Supersedes:     str(row["supersedes"]),
		Contradicts:    strs(row["contradicts"]),
		PromotedFrom:   str(row["promoted_from"]),
		Evidence:       strs(row["evidence"]),
		ApprovedBy:     str(row["approved_by"]),
	}.Normalize(str(row["written_by"]))
}

func suggested(n notes.Note) string {
	if n.SuggestedLevel.Valid() {
		return string(n.SuggestedLevel)
	}
	return string(n.Level)
}

func count(res omnigraph.Result, column string) int {
	if len(res.Rows) == 0 {
		return 0
	}
	switch v := res.Rows[0][column].(type) {
	case float64:
		return int(v)
	case string:
		c, _ := strconv.Atoi(v)
		return c
	}
	return 0
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

func nilIfNone(list []string) any {
	if len(list) == 0 {
		return nil
	}
	return list
}

func nilIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
