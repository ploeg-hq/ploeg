package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/boardview"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// recordBoardClaim records the Ploeg sequence and tracker revision a Run's
// briefing is read at (ADR-0124). A failure is logged and the claim goes
// ahead without them: the Run then neither reports its briefing nor catches
// up.
func (s *Server) recordBoardClaim(ctx context.Context, runToken string) *work.BoardVersions {
	v, err := s.Store.RecordBoardClaim(ctx, runToken)
	if err != nil {
		s.logger().Warn("board versions not recorded at the claim; the Run is not caught up", "err", err)
		return nil
	}
	return &v
}

// shiftBriefing assembles a Shift Run's findings briefing from what the
// Shift recorded: the restart first, then earlier Rounds' findings, the
// reviews, and what changed on the board while earlier Runs worked (ADR-0124).
// Prior Rounds only: Runs in one Round never observe each other (ADR-0010).
// The claim and the replay both build it here, so they agree.
func shiftBriefing(in store.ReplayInputs, round int) []harness.Finding {
	var out []harness.Finding
	if in.Restart != nil {
		out = append(out, restartBriefing(*in.Restart))
	}
	for _, rep := range in.Reports {
		if rep.Round < round && rep.Findings != "" && !rep.Shadow {
			out = append(out, harness.Finding{Role: rep.Role, Round: rep.Round, Findings: rep.Findings})
		}
	}
	out = append(out, reviewBriefing(in.Reviews)...)
	for _, c := range in.CatchUps {
		out = append(out, boardview.NextRoundFinding(c.Role, c.Round, c.Changes))
	}
	return out
}

const boardRequestLimit = 4 << 10

// handleRunBoard answers a running Run what changed on the board since its
// briefing (ADR-0124). The Run's versions come from its record, never from
// the request; 409 answers a Run claimed before its versions were recorded.
func (s *Server) handleRunBoard(w http.ResponseWriter, r *http.Request) {
	var req work.BoardChangesRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, boardRequestLimit)).Decode(&req); err != nil {
		http.Error(w, "body must be a JSON boardChangesRequest", http.StatusBadRequest)
		return
	}
	switch req.Checkpoint {
	case work.CatchUpRefresh, work.CatchUpBeforeFinish:
	default:
		http.Error(w, "checkpoint must be refresh or before_finish", http.StatusBadRequest)
		return
	}
	switch req.Delivery {
	case work.DeliveryFile, work.DeliveryPrompt, work.DeliveryNextRound:
	default:
		http.Error(w, "delivery must be file, prompt or next_round", http.StatusBadRequest)
		return
	}
	concepts := ""
	if s.Knowledge != nil {
		if scope, err := s.Store.KnowledgeScopeFor(r.Context(), r.PathValue("token")); err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), notesReadTimeout)
			if _, commit, err := s.Knowledge.Concepts(ctx, scope.Client, scope.Repository); err == nil {
				concepts = commit
			}
			cancel()
		}
	}
	resp, err := s.Store.BoardChanges(r.Context(), r.PathValue("token"), req, concepts)
	switch {
	case errors.Is(err, store.ErrNoBoardVersions):
		http.Error(w, "this Run recorded no board versions", http.StatusConflict)
		return
	case err != nil:
		runError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type briefingFindings struct {
	Items          int    `json:"items"`
	Digest         string `json:"digest"`
	RecordedDigest string `json:"recordedDigest,omitempty"`
	Matches        *bool  `json:"matches,omitempty"`
	Untrusted      struct {
		Text string `json:"text"`
	} `json:"untrusted"`
}

type briefingNote struct {
	Key       string    `json:"key"`
	Type      string    `json:"type"`
	WrittenBy string    `json:"writtenBy,omitempty"`
	WrittenAt time.Time `json:"writtenAt,omitzero"`
	Untrusted struct {
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		Body        string `json:"body"`
	} `json:"untrusted"`
}

type briefingResponse struct {
	SchemaVersion string                 `json:"schemaVersion"`
	RunID         string                 `json:"runId"`
	Recorded      bool                   `json:"recorded"`
	ClaimedAt     *time.Time             `json:"claimedAt,omitempty"`
	BriefedAt     *time.Time             `json:"briefedAt,omitempty"`
	Versions      *work.BoardVersions    `json:"versions,omitempty"`
	Sections      []work.BriefingSection `json:"sections"`
	Findings      *briefingFindings      `json:"findings,omitempty"`
	Notes         []briefingNote         `json:"notes"`
	MissingNotes  []string               `json:"missingNotes"`
	Concepts      []string               `json:"concepts"`
	CatchUps      []store.CatchUpRecord  `json:"catchUps"`
}

// handleOperatorRunBriefing replays a Run's briefing from what it recorded
// (ADR-0124): its version vector, its findings section rebuilt from the
// records its claim read and checked against the digest the worker
// reported, the Work Item Notes it showed read back by key, the concepts it
// showed as pointers at their graph commit, and every catch-up it was given.
// A Run recorded before ADR-0124 answers with recorded false.
func (s *Server) handleOperatorRunBriefing(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, err := store.OperatorCursor(r.PathValue("id"))
	if err != nil || id == 0 || r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "Invalid resource identifier or unknown query parameter.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	rec, err := s.Store.OperatorBriefing(r.Context(), id, principal.Teams)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	resp := briefingResponse{SchemaVersion: "1.0", RunID: strconv.FormatInt(id, 10), Recorded: rec.ClaimedAt != nil,
		ClaimedAt: utcPtr(rec.ClaimedAt), BriefedAt: utcPtr(rec.BriefedAt), Sections: rec.Sections, Notes: []briefingNote{},
		MissingNotes: []string{}, Concepts: rec.Concepts, CatchUps: rec.CatchUps}
	if resp.Sections == nil {
		resp.Sections = []work.BriefingSection{}
	}
	if resp.CatchUps == nil {
		resp.CatchUps = []store.CatchUpRecord{}
	}
	if !resp.Recorded {
		resp.Concepts = []string{}
		operatorJSON(w, 200, resp)
		return
	}
	resp.Versions = &rec.Versions
	in, err := s.Store.BriefingReplayInputs(r.Context(), rec)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	findings := shiftBriefing(in, rec.Round)
	if rec.Role != work.AskRole {
		for _, n := range in.Operator {
			findings = append(findings, operatorNoteBriefing(n, rec.Round))
		}
	}
	text, section := boardview.Findings(findings)
	f := &briefingFindings{Items: section.Items, Digest: section.Digest}
	f.Untrusted.Text = text
	for _, sec := range rec.Sections {
		if sec.Name == work.SectionFindings {
			f.RecordedDigest = sec.Digest
			matches := sec.Digest == section.Digest
			f.Matches = &matches
		}
	}
	resp.Findings = f
	notes, err := s.Store.NotesByKey(r.Context(), rec.Notes)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	found := map[string]bool{}
	for _, n := range notes {
		found[n.Key] = true
		b := briefingNote{Key: n.Key, Type: n.Type, WrittenBy: n.WrittenBy, WrittenAt: n.WrittenAt}
		b.Untrusted.Title, b.Untrusted.Description, b.Untrusted.Body = n.Title, n.Description, n.Body
		resp.Notes = append(resp.Notes, b)
	}
	for _, k := range rec.Notes {
		if !found[k] {
			resp.MissingNotes = append(resp.MissingNotes, k)
		}
	}
	operatorJSON(w, 200, resp)
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
