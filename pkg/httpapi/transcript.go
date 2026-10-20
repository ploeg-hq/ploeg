package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/transcript"
)

// Bounds of the transcript routes (ADR-0090).
const (
	maxTranscriptRequestBytes = 1 << 20
	defaultTranscriptPage     = 200
	maxTranscriptPage         = 500
	transcriptClockSkew       = 5 * time.Minute
)

type transcriptRequest struct {
	Entries []transcript.Entry `json:"entries"`
}

type transcriptAppendResponse struct {
	Accepted  int  `json:"accepted"`
	Truncated bool `json:"truncated"`
}

// handleRunTranscript stores a batch of the calling Run's transcript. The
// worker middleware has already authenticated the run token. Entries are
// clamped and redacted again here, so what is stored never depends on the
// worker having done it.
func (s *Server) handleRunTranscript(w http.ResponseWriter, r *http.Request) {
	if s.DisableRunTranscripts {
		http.NotFound(w, r)
		return
	}
	var req transcriptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTranscriptRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "transcript requires an entries array", http.StatusBadRequest)
		return
	}
	if err := validateTranscript(req.Entries); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := r.PathValue("token")
	redact := transcript.NewRedactor(token, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	now := time.Now().UTC()
	for i, e := range req.Entries {
		if e.At.IsZero() || e.At.After(now.Add(transcriptClockSkew)) {
			e.At = now
		}
		req.Entries[i] = redact.Seal(e)
	}
	res, err := s.Store.AppendTranscript(r.Context(), token, req.Entries)
	if err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, transcriptAppendResponse{Accepted: res.Accepted, Truncated: res.Truncated})
}

func validateTranscript(entries []transcript.Entry) error {
	if len(entries) == 0 || len(entries) > transcript.MaxBatchEntries {
		return errors.New("transcript batch must hold 1 to " + strconv.Itoa(transcript.MaxBatchEntries) + " entries")
	}
	for _, e := range entries {
		if e.Seq < 1 || e.Seq > transcript.MaxSeq {
			return errors.New("transcript entry seq must be a positive sequence number")
		}
		if !e.Kind.Valid() {
			return errors.New("transcript entry kind must be message, thought, tool_call, tool_output, plan, stage or truncated")
		}
	}
	return nil
}

type transcriptResponse struct {
	SchemaVersion string             `json:"schemaVersion"`
	RunID         string             `json:"runId"`
	Entries       []transcript.Entry `json:"entries"`
	NextAfter     int64              `json:"nextAfter"`
	HasMore       bool               `json:"hasMore"`
	Truncated     bool               `json:"truncated"`
	Complete      bool               `json:"complete"`
}

// handleOperatorRunTranscript serves one page of a Run's transcript to an
// operator consumer that may read the Run's team.
func (s *Server) handleOperatorRunTranscript(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, err := store.OperatorCursor(r.PathValue("id"))
	if err != nil || id == 0 {
		operatorError(w, 400, "invalid_request", "Invalid resource identifier.")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return
	}
	for key, values := range q {
		if (key != "after" && key != "limit") || len(values) != 1 || values[0] == "" {
			operatorError(w, 400, "invalid_request", "Unknown, empty or repeated query parameter.")
			return
		}
	}
	after, err := store.OperatorCursor(q.Get("after"))
	if err != nil || after > transcript.MaxSeq {
		operatorError(w, 400, "invalid_request", "after must be a sequence number.")
		return
	}
	limit := defaultTranscriptPage
	if v := q.Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > maxTranscriptPage {
			operatorError(w, 400, "invalid_request", "Limit must be between 1 and "+strconv.Itoa(maxTranscriptPage)+".")
			return
		}
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	page, err := s.Store.OperatorTranscript(r.Context(), id, principal.Teams, after, limit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	next := after
	if n := len(page.Entries); n > 0 {
		next = page.Entries[n-1].Seq
	}
	operatorJSON(w, 200, transcriptResponse{
		SchemaVersion: "1.0", RunID: strconv.FormatInt(id, 10), Entries: page.Entries,
		NextAfter: next, HasMore: page.HasMore, Truncated: page.Truncated, Complete: page.Complete,
	})
}
