package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/contextbundle"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Context bundles (proposed, proof of concept): a person attaches files to a
// Work Item, and every Run claimed afterwards downloads and reads them.

// DefaultContextMaxBytes bounds one upload (PLOEG_CONTEXT_MAX_BYTES).
const DefaultContextMaxBytes int64 = 20 << 20

// DefaultContextMaxTotalBytes bounds a Work Item's context together
// (PLOEG_CONTEXT_MAX_TOTAL_BYTES).
const DefaultContextMaxTotalBytes int64 = 50 << 20

const maxContextNoteRunes = 500

func (s *Server) contextMaxBytes() int64 {
	if s.ContextMaxBytes > 0 {
		return s.ContextMaxBytes
	}
	return DefaultContextMaxBytes
}

func (s *Server) contextMaxTotalBytes() int64 {
	if s.ContextMaxTotalBytes > 0 {
		return s.ContextMaxTotalBytes
	}
	return DefaultContextMaxTotalBytes
}

func (s *Server) registerContext(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/context", s.handleAddWorkItemContext)
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}/context", s.handleListWorkItemContext)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/context", s.handleAddExecutionContext)
}

func isContextUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/operator/") && strings.HasSuffix(r.URL.Path, "/context")
}

func (s *Server) handleAddWorkItemContext(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, err := store.OperatorCursor(r.PathValue("id"))
	if err != nil || id == 0 {
		operatorError(w, 400, "invalid_request", "Invalid resource identifier.")
		return
	}
	s.addContext(w, r, p, actor, id)
}

func (s *Server) handleAddExecutionContext(w http.ResponseWriter, r *http.Request) {
	p, e, ok := s.executionForRequest(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(e.WorkItemID, 10, 64)
	if err != nil || id == 0 {
		operatorError(w, 404, "not_found", "The execution has no Work Item.")
		return
	}
	s.addContext(w, r, p, e.Actor, id)
}

func (s *Server) addContext(w http.ResponseWriter, r *http.Request, p OperatorPrincipal, actor string, workItemID int64) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return
	}
	for key, values := range q {
		if (key != "name" && key != "note") || len(values) != 1 {
			operatorError(w, 400, "invalid_request", "Only name and note may be given, once each.")
			return
		}
	}
	name, err := contextbundle.CleanName(q.Get("name"))
	if err != nil {
		operatorError(w, 400, "invalid_name", "The file name "+strings.TrimPrefix(err.Error(), "name: ")+".")
		return
	}
	note := strings.TrimSpace(q.Get("note"))
	if !validNote(note) {
		operatorError(w, 400, "invalid_note", "The note must be valid text of at most 500 characters.")
		return
	}
	limit := s.contextMaxBytes()
	if r.ContentLength > limit {
		operatorError(w, 413, "too_large", "The file is larger than the "+strconv.FormatInt(limit, 10)+" bytes one upload may hold.")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit+1))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || int64(len(data)) > limit {
		operatorError(w, 413, "too_large", "The file is larger than the "+strconv.FormatInt(limit, 10)+" bytes one upload may hold.")
		return
	}
	if err != nil {
		operatorError(w, 400, "invalid_request", "The upload could not be read.")
		return
	}
	manifest, err := contextbundle.Inspect(name, data, contextbundle.DefaultLimits())
	if err != nil {
		operatorError(w, 400, "unsafe_bundle", "Ploeg refused the file: "+err.Error()+".")
		return
	}
	addedBy := actor
	if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
		addedBy = acting
	}
	sum := sha256.Sum256(data)
	c, created, err := s.Store.AddWorkItemContext(r.Context(), store.NewWorkItemContext{
		WorkItemID: workItemID, Teams: p.Teams, Name: name, MediaType: manifest.MediaType,
		SHA256: hex.EncodeToString(sum[:]), Files: len(manifest.Files), Content: data, Note: note,
		AddedBy: addedBy, Actor: "operator:" + p.Name + ":" + actor, MaxTotalBytes: s.contextMaxTotalBytes(),
	})
	switch {
	case errors.Is(err, store.ErrOperatorNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrContextTerminal):
		operatorError(w, 409, "work_item_finished", "The Work Item is done or withdrawn; no Run will read new context.")
		return
	case errors.Is(err, store.ErrContextTooLarge):
		operatorError(w, 413, "total_too_large", "The Work Item's context would exceed "+strconv.FormatInt(s.contextMaxTotalBytes(), 10)+" bytes.")
		return
	case err != nil:
		if s.Log != nil {
			s.Log.Error("context upload failed", "work_item", workItemID, "err", err)
		}
		operatorError(w, 503, "unavailable", "Ploeg could not store the context.")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	if created && s.Log != nil {
		s.Log.Info("context attached", "work_item", workItemID, "context", c.ID, "files", c.Files, "bytes", c.Bytes, "phase", c.Phase)
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "context": c})
}

func validNote(note string) bool {
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > maxContextNoteRunes {
		return false
	}
	return strings.IndexFunc(note, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}

func (s *Server) handleListWorkItemContext(w http.ResponseWriter, r *http.Request) {
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	items, err := s.Store.ListWorkItemContext(r.Context(), id, principal.Teams, time.Time{})
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "context": items})
}

// handleRunContext serves one context item's bytes to the Run it was listed
// for. Anything else, another Work Item's item included, is 404.
func (s *Server) handleRunContext(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.RunContextItem(r.Context(), r.PathValue("token"), r.PathValue("id"))
	if errors.Is(err, store.ErrUnknownRun) {
		http.Error(w, "unknown context item", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "context read failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(c.Content)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(c.Content)
}

// claimContext lists what a claimed Run is given. A failed read is logged
// and the Run proceeds without context, as it does without a briefing.
func (s *Server) claimContext(ctx context.Context, runToken string) []harness.ContextRef {
	items, err := s.Store.RunContext(ctx, runToken)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("context read failed; run proceeds without it", "err", err)
		}
		return nil
	}
	var refs []harness.ContextRef
	for _, c := range items {
		refs = append(refs, harness.ContextRef{ID: c.ID, Name: c.Name, MediaType: c.MediaType, SHA256: c.SHA256,
			Bytes: c.Bytes, Files: c.Files, Note: c.Note, AddedAt: c.AddedAt, Phase: c.Phase})
	}
	return refs
}
