package harness

import (
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0124: the version vector on the claim, the briefed checkpoint and the
// board catch-up bodies are on the published contract, and an out-of-bounds
// value is refused by it.
func TestBoardBodies_MatchTheContract(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	versions := work.BoardVersions{ForgeCommit: sha, PloegSequence: 42, NotesCommit: "c1", ConceptsCommit: "c2", TrackerRevision: "7"}
	claim := map[string]any{"runToken": strings.Repeat("a1", 24), "deadline": "2026-10-04T10:00:00Z",
		"workItem": fullTaskSpec().WorkItem, "board": work.BoardVersions{PloegSequence: 42, TrackerRevision: "7"}}
	cp := work.Checkpoint{Phase: work.PhaseBriefed, Branch: "agent/vik-596", Board: &work.BoardBriefing{
		ForgeCommit: sha, NotesCommit: "c1", ConceptsCommit: "c2", Notes: []string{"ploeg-abc/3"}, Concepts: []string{"repo/a.md"},
		Sections: []work.BriefingSection{{Name: work.SectionFindings, Budget: 8000, Bytes: 120, Items: 2, Digest: "sha256:0123456789abcdef"},
			{Name: work.SectionWorkItem, Budget: 9000, Bytes: 0, Items: 0}}}}
	resp := work.BoardChangesResponse{Since: versions, Current: versions, Changes: []work.BoardChange{
		{Source: work.SourceTracker, Kind: "work_item_changed", At: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC), Cite: "tracker revision 8",
			Summary: "The tracker task changed.", Untrusted: "Title: x"}}, Omitted: 1}
	for def, body := range map[string]any{
		"run-api.v1.schema.json#/$defs/claimResponse":        claim,
		"checkpoint.v1.schema.json":                          cp,
		"run-api.v1.schema.json#/$defs/boardChangesRequest":  work.BoardChangesRequest{Checkpoint: work.CatchUpBeforeFinish, Delivery: work.DeliveryNextRound, ForgeHead: sha, ForgeMovedByOthers: true},
		"run-api.v1.schema.json#/$defs/boardChangesResponse": resp,
		"run-api.v1.schema.json#/$defs/boardChangesResponse#empty": work.BoardChangesResponse{Since: versions, Current: versions,
			Changes: []work.BoardChange{}},
	} {
		name, _, _ := strings.Cut(def, "#empty")
		if err := validate(t, compileSchema(t, name), body); err != nil {
			t.Errorf("%s violates the contract: %v", def, err)
		}
	}
	bad := cp
	bad.Board = &work.BoardBriefing{Sections: []work.BriefingSection{{Name: "everything", Budget: 1}}}
	if err := validate(t, compileSchema(t, "checkpoint.v1.schema.json"), bad); err == nil {
		t.Error("a briefing section with an unknown name validated")
	}
	if err := validate(t, compileSchema(t, "run-api.v1.schema.json#/$defs/boardChangesRequest"),
		work.BoardChangesRequest{Checkpoint: "whenever", Delivery: work.DeliveryFile}); err == nil {
		t.Error("an unknown checkpoint validated")
	}
}
