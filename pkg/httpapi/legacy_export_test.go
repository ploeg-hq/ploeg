package httpapi

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

type legacyBody struct {
	Deprecated bool `json:"deprecated"`
	Items      []struct {
		WorkItemID string `json:"workItemId"`
		Cracks     []struct {
			State     string  `json:"state"`
			Severity  *string `json:"severity"`
			MendedBy  *string `json:"mendedBy"`
			EvolvedBy *string `json:"evolvedBy"`
			Bug       struct {
				ExternalID string `json:"externalId"`
			} `json:"bug"`
			PullRequest *struct {
				Number int `json:"number"`
			} `json:"pullRequest"`
		} `json:"cracks"`
		Rarity *struct {
			RevealedTier string `json:"revealedTier"`
			CohortSize   int    `json:"cohortSize"`
		} `json:"rarity"`
		Comment *struct {
			CommentID *int64 `json:"commentId"`
			Moment    string `json:"moment"`
		} `json:"comment"`
		Shapes []struct {
			Shape map[string]any `json:"shape"`
		} `json:"shapes"`
	} `json:"items"`
	NextAfter *string `json:"nextAfter"`
}

func legacyGET(t *testing.T, s *Server, token, query string) legacyBody {
	t.Helper()
	w := operatorDo(t, s, token, "GET", "/api/v1/operator/card-legacy-export?"+query, "", nil)
	if w.Code != 200 || w.Header().Get("Deprecation") != "true" {
		t.Fatalf("export %s: %d %s", query, w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	var body legacyBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestLegacyExport_ReturnsEveryRowTypeAndPages(t *testing.T) {
	s, token := factsServer(t, []string{"silver"}, false)
	now := time.Now().UTC().Truncate(time.Second)
	card, play := plainFactsItem(t, "legacy-card", "silver", 11, "anna", now.Add(-48*time.Hour))
	bug, mend := plainFactsItem(t, "legacy-bug", "silver", 12, "bram", now.Add(-24*time.Hour))
	other, _ := plainFactsItem(t, "legacy-other", "silver", 13, "anna", now.Add(-12*time.Hour))
	unrevealed, _ := plainFactsItem(t, "legacy-unrevealed", "silver", 14, "anna", now.Add(-6*time.Hour))
	gold, goldPlay := plainFactsItem(t, "legacy-gold", "gold", 15, "anna", now)

	execSQL(t, `INSERT INTO card_cracks (team, card_work_item_id, pull_request_id, bug_work_item_id, state, severity, share, discovery,
		steward, proposed_by, confirmed_by, confirmed_at, mend_pull_request_id, mend_number, mended_at, mended_by, mend_by_steward)
		VALUES ('silver', $1, $2, $3, 'confirmed', 'S2', 'primary', 'discovered', 'anna', 'bram', 'carla', $4, $5, 12, $4, 'bram', false)`,
		card, play, bug, now, mend)
	execSQL(t, `INSERT INTO card_cracks (team, card_work_item_id, bug_work_item_id, state, proposed_by, evolved_by, evolved_at)
		VALUES ('silver', $1, $2, 'evolved', 'bram', 'dirk', $3)`, card, other, now)
	execSQL(t, `INSERT INTO card_rarity (work_item_id, checked_at, formula, revealed_tier, predicted_tier, score, predicted_score,
		cohort_target, cohort_quarter, cohort_size, inputs, revealed_at, recorded_at)
		VALUES ($1, $2, '2026.1', 'rare', 'uncommon', 61.5, 40.0, 'webgrip/ploeg', '2026Q4', 3, '{"files": 4}', $2, $2)`, card, now)
	execSQL(t, `INSERT INTO card_rarity (work_item_id, checked_at) VALUES ($1, $2)`, unrevealed, now)
	execSQL(t, `INSERT INTO card_comments (work_item_id, pull_request_id, moment, comment_id, image, published_at, checked_at)
		VALUES ($1, $2, 'merged', 901, true, $3, $3)`, card, play, now)
	execSQL(t, `UPDATE pull_requests SET shape = '{"files": 2, "complexity": {"added": 7}}' WHERE id = $1`, play)
	execSQL(t, `INSERT INTO card_comments (work_item_id, pull_request_id, moment, checked_at) VALUES ($1, $2, '', $3)`, other, nil, now)
	execSQL(t, `INSERT INTO card_comments (work_item_id, pull_request_id, moment, checked_at) VALUES ($1, $2, 'merged', $3)`, gold, goldPlay, now)

	first := legacyGET(t, s, token, "limit=1")
	if !first.Deprecated || len(first.Items) != 1 || first.NextAfter == nil || first.Items[0].WorkItemID != strconv.FormatInt(card, 10) {
		t.Fatalf("first page = %+v", first)
	}
	item := first.Items[0]
	if len(item.Cracks) != 2 || item.Cracks[0].State != "confirmed" || item.Cracks[0].Severity == nil || *item.Cracks[0].MendedBy != "bram" ||
		item.Cracks[0].Bug.ExternalID != "legacy-bug" || item.Cracks[0].PullRequest == nil || item.Cracks[0].PullRequest.Number != 11 ||
		item.Cracks[1].State != "evolved" || item.Cracks[1].Severity != nil || *item.Cracks[1].EvolvedBy != "dirk" {
		t.Errorf("cracks = %+v", item.Cracks)
	}
	if item.Rarity == nil || item.Rarity.RevealedTier != "rare" || item.Rarity.CohortSize != 3 {
		t.Errorf("rarity = %+v", item.Rarity)
	}
	if item.Comment == nil || item.Comment.CommentID == nil || *item.Comment.CommentID != 901 || item.Comment.Moment != "merged" {
		t.Errorf("comment = %+v", item.Comment)
	}
	if len(item.Shapes) != 1 || item.Shapes[0].Shape["files"] != float64(2) {
		t.Errorf("shapes = %+v", item.Shapes)
	}

	second := legacyGET(t, s, token, "limit=1&after="+*first.NextAfter)
	if len(second.Items) != 1 || second.Items[0].WorkItemID != strconv.FormatInt(other, 10) || second.NextAfter != nil {
		t.Fatalf("second page = %+v (the unrevealed rarity and the other team stay out)", second)
	}
	if second.Items[0].Rarity != nil || second.Items[0].Comment == nil || len(second.Items[0].Cracks) != 0 {
		t.Errorf("second item = %+v", second.Items[0])
	}
	all := legacyGET(t, s, token, "")
	if len(all.Items) != 2 {
		t.Errorf("whole export = %+v", all)
	}
	for _, bad := range []string{"limit=0", "limit=201", "after=x", "page=2"} {
		if w := operatorDo(t, s, token, "GET", "/api/v1/operator/card-legacy-export?"+bad, "", nil); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}
