package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// CardCondition is what confirmed cracks did to a card (ADR-0052). State is
// cracked while any crack's mend is unconfirmed, and mended once every crack
// has a confirmed mend. A card without a confirmed crack has no condition.
type CardCondition struct {
	State  string      `json:"state"`
	Cracks []CardCrack `json:"cracks"`
}

// CardCrack is one confirmed attribution of a bug to the card. Weight is
// what it takes off reliability under formula GradeFormula; Warranty is
// full, half or history by the card's age when the bug Work Item was
// created. Disputed is true while a referee has not decided.
type CardCrack struct {
	ID          string         `json:"id"`
	Bug         CardCrackBug   `json:"bug"`
	Severity    string         `json:"severity"`
	Share       string         `json:"share"`
	Discovery   string         `json:"discovery"`
	ProposedAt  time.Time      `json:"proposedAt"`
	ConfirmedAt time.Time      `json:"confirmedAt"`
	ConfirmedBy []string       `json:"confirmedBy"`
	Disputed    bool           `json:"disputed"`
	Weight      float64        `json:"weight"`
	Warranty    string         `json:"warranty"`
	Mended      *CardCrackMend `json:"mended"`
}

// CardCrackBug is the bug Work Item a crack came from.
type CardCrackBug struct {
	WorkItemID string `json:"workItemId"`
	Ref        string `json:"ref,omitempty"`
	Title      string `json:"title"`
}

// CardCrackMend is the merged fix of a crack. By is left out when the fix
// was merged by a login Ploeg acts as. ConfirmedAt is null until the mend
// stood MendWindow with no new crack.
type CardCrackMend struct {
	At          time.Time  `json:"at"`
	By          string     `json:"by,omitempty"`
	PR          int        `json:"pr"`
	BySteward   bool       `json:"bySteward"`
	ConfirmedAt *time.Time `json:"confirmedAt"`
}

// DefaultHotfixLabel is the pull request label that marks a fix as a
// hotfix for a team that names none.
const DefaultHotfixLabel = "hotfix"

type cardCrack struct {
	crack      CardCrack
	bugCreated time.Time
	primaries  int
	mendBy     string
}

var severityWeights = map[string]float64{"S1": 4, "S2": 2, "S3": 1, "S4": 0.25}
var discoveryFactors = map[string]float64{"self": 0.5, "discovered": 1, "concealed": 1.5}

const (
	contributingShare = 0.25
	stewardMendFactor = 0.5
	otherMendFactor   = 0.75
	revertFloorWeight = 2
	crackedCeiling    = 9.5
	warrantyFullDays  = 180
	warrantyHalfDays  = 365
	penaltyRevert     = 2.0
	penaltyHotfix     = 1.0
)

func warranty(liveSince *time.Time, bugCreated time.Time) (string, float64) {
	if liveSince == nil || !bugCreated.After(*liveSince) {
		return "full", 1
	}
	days := int(bugCreated.Sub(*liveSince) / (24 * time.Hour))
	switch {
	case days <= warrantyFullDays:
		return "full", 1
	case days <= warrantyHalfDays:
		return "half", 0.5
	default:
		return "history", 0
	}
}

func crackWeight(severity, share, discovery string, primaries int, warrantyFactor float64, mend *CardCrackMend) float64 {
	shareFactor := contributingShare
	if share == "primary" {
		shareFactor = 1 / float64(max(1, primaries))
	}
	mendFactor := 1.0
	if mend != nil && mend.ConfirmedAt != nil {
		mendFactor = otherMendFactor
		if mend.BySteward {
			mendFactor = stewardMendFactor
		}
	}
	return severityWeights[severity] * shareFactor * discoveryFactors[discovery] * warrantyFactor * mendFactor
}

func (c *OperatorCard) loadCondition(ctx context.Context, tx pgx.Tx, id int64, hotfixLabels []string) error {
	rows, err := tx.Query(ctx, `SELECT c.id, c.state, c.severity, c.share, c.discovery, c.proposed_by, c.proposed_at,
			c.confirmed_by, c.confirmed_at, c.mend_number, c.mended_at, COALESCE(c.mended_by, ''), COALESCE(c.mend_by_steward, false),
			c.mend_confirmed_at, b.id, b.provider, b.external_id, left(b.title, 4096), b.created_at,
			(SELECT count(*) FROM card_cracks o WHERE o.bug_work_item_id = c.bug_work_item_id AND o.share = 'primary'
			   AND o.state IN ('confirmed', 'disputed'))
		FROM card_cracks c JOIN work_items b ON b.id = c.bug_work_item_id
		WHERE c.card_work_item_id = $1 AND c.state IN ('confirmed', 'disputed') AND c.confirmed_at IS NOT NULL
		ORDER BY c.confirmed_at, c.id LIMIT $2`, id, maxCracksListed)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k                      cardCrack
			crackID, bugID         int64
			state, proposedBy      string
			confirmedBy            string
			mendNumber             *int
			mendedAt, mendConfirm  *time.Time
			mendedBy               string
			bySteward              bool
			bugProvider, bugExtern string
		)
		if err := rows.Scan(&crackID, &state, &k.crack.Severity, &k.crack.Share, &k.crack.Discovery, &proposedBy, &k.crack.ProposedAt,
			&confirmedBy, &k.crack.ConfirmedAt, &mendNumber, &mendedAt, &mendedBy, &bySteward, &mendConfirm,
			&bugID, &bugProvider, &bugExtern, &k.crack.Bug.Title, &k.bugCreated, &k.primaries); err != nil {
			return err
		}
		k.crack.ID = strconv.FormatInt(crackID, 10)
		k.crack.Bug.WorkItemID = strconv.FormatInt(bugID, 10)
		k.crack.Bug.Ref = itemRef(bugProvider, bugExtern)
		k.crack.ProposedAt, k.crack.ConfirmedAt = k.crack.ProposedAt.UTC(), k.crack.ConfirmedAt.UTC()
		k.crack.ConfirmedBy = []string{proposedBy, confirmedBy}
		k.crack.Disputed = state == "disputed"
		if mendedAt != nil && mendNumber != nil {
			k.crack.Mended = &CardCrackMend{At: mendedAt.UTC(), By: mendedBy, PR: *mendNumber, BySteward: bySteward, ConfirmedAt: mendConfirm}
			k.mendBy = mendedBy
		}
		c.cracks = append(c.cracks, k)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM card_cracks WHERE card_work_item_id = $1 AND state = 'evolved')`, id).
		Scan(&c.evolvedByAttribution); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pull_request_reverts r JOIN pull_requests p ON p.id = r.pull_request_id
		WHERE p.work_item_id = $1`, id).Scan(&c.reverts); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT count(DISTINCT p.id) FROM card_cracks c JOIN pull_requests p ON p.work_item_id = c.bug_work_item_id
		WHERE c.card_work_item_id = $1 AND c.state IN ('confirmed', 'disputed') AND p.state = 'merged'
		  AND EXISTS (SELECT 1 FROM unnest(COALESCE(p.labels, '{}')) l WHERE lower(l) = ANY($2))`, id, hotfixLabels).Scan(&c.hotfixes)
}

func hotfixLabelsFor(opts CardOptions, team string) []string {
	labels, ok := opts.HotfixLabels[team]
	if !ok || len(labels) == 0 {
		return []string{DefaultHotfixLabel}
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, strings.ToLower(l))
	}
	return out
}

func (c *OperatorCard) condition() (*CardCondition, float64, bool) {
	var liveSince *time.Time
	if c.Release != nil {
		at := c.Release.At
		liveSince = &at
	}
	if len(c.cracks) == 0 {
		return nil, 0, false
	}
	cond := &CardCondition{State: "mended", Cracks: make([]CardCrack, 0, len(c.cracks))}
	total, inWarranty := 0.0, false
	for _, k := range c.cracks {
		cr := k.crack
		if cr.Mended != nil && !c.human(cr.Mended.By) {
			cr.Mended.By = ""
		}
		var factor float64
		cr.Warranty, factor = warranty(liveSince, k.bugCreated)
		cr.Weight = crackWeight(cr.Severity, cr.Share, cr.Discovery, k.primaries, factor, cr.Mended)
		total += cr.Weight
		if cr.Warranty != "history" {
			inWarranty = true
		}
		if cr.Mended == nil || cr.Mended.ConfirmedAt == nil {
			cond.State = "cracked"
		}
		cond.Cracks = append(cond.Cracks, cr)
	}
	return cond, total, inWarranty
}
