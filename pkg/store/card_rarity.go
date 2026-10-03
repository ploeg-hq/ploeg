package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/rarity"
)

// CardRarity is how exceptional the card's change was (ADR-0056): a
// challenge score under formula Formula, predicted from what was known
// before the merge and revealed from the real change once the card is
// released. Tier is Revealed when revealed and Predicted otherwise, except
// that an epic's own card is legendary while its set is complete. Score,
// Percentile, Cohort and Inputs describe the revealed moment once revealed
// and the predicted one before. A revealed card's tier, score and cohort are
// frozen when it is revealed. Percentile is nil while the cohort is smaller
// than rarity.MinCohort and fixed thresholds decide. Cohort is nil when the
// card has no repository to compare in.
type CardRarity struct {
	Formula    string            `json:"formula"`
	Predicted  *string           `json:"predicted"`
	Revealed   *string           `json:"revealed"`
	Tier       string            `json:"tier"`
	Score      *float64          `json:"score"`
	Percentile *float64          `json:"percentile"`
	Cohort     *CardRarityCohort `json:"cohort"`
	Inputs     CardRarityInputs  `json:"inputs"`
	RevealedAt *time.Time        `json:"revealedAt"`
}

// CardRarityCohort is the set of cards a score is ranked in: the cards of
// Target, a lowercased "owner/name", revealed in Quarter (as "2026Q4"),
// under the same formula. Size counts them with the card.
type CardRarityCohort struct {
	Target  string `json:"target"`
	Quarter string `json:"quarter"`
	Size    int    `json:"size"`
}

// CardRarityInputs are the facts the score used. A nil fact is one Ploeg
// does not know, and it adds nothing to the score. Set is whether the card
// is in an epic's set, and is nil on a revealed score, which it does not
// move. A file the Work Target leaves out of size counts for none of them.
// Truncated is true when a play touched more files than Ploeg keeps,
// so the file facts are a lower bound. NotCollected names the inputs Ploeg
// has no source for yet.
type CardRarityInputs struct {
	Reach        CardRarityReach     `json:"reach"`
	Sensitive    CardRaritySensitive `json:"sensitive"`
	Novelty      CardRarityNovelty   `json:"novelty"`
	Size         CardRaritySize      `json:"size"`
	Set          *bool               `json:"set"`
	Truncated    bool                `json:"truncated"`
	NotCollected []string            `json:"notCollected"`
}

// CardRarityReach: Modules counts the distinct modules touched, Repos the
// distinct repositories.
type CardRarityReach struct {
	Modules *int `json:"modules"`
	Repos   *int `json:"repos"`
}

// CardRaritySensitive: Files counts the touched files on sensitive ground;
// Paths lists up to cardRarityPaths of them, sorted.
type CardRaritySensitive struct {
	Files *int     `json:"files"`
	Paths []string `json:"paths"`
}

// CardRarityNovelty: Novel counts the touched files no earlier merged play
// of the same repository touched within the novelty window; Share is Novel
// over Files.
type CardRarityNovelty struct {
	Share *float64 `json:"share"`
	Files *int     `json:"files"`
	Novel *int     `json:"novel"`
}

// CardRaritySize: CountedLines adds the lines added and removed, without
// the files the Work Target leaves out of size.
type CardRaritySize struct {
	CountedLines *int64 `json:"countedLines"`
}

// RarityOptions is what computing a card's rarity takes from
// configuration. Matchers holds, keyed by lowercased "owner/name", the path
// rules of every Work Target that sets them; any other repository uses the
// defaults.
type RarityOptions struct {
	Matchers map[string]rarity.Matcher
}

const cardRarityPaths = 20

var defaultMatcher = func() rarity.Matcher {
	m, err := rarity.Rules{}.Compile()
	if err != nil {
		panic(err)
	}
	return m
}()

func (o *RarityOptions) matcher(repo string) rarity.Matcher {
	if m, ok := o.Matchers[strings.ToLower(repo)]; ok {
		return m
	}
	return defaultMatcher
}

type rarityFile struct {
	path               string
	additions, deletes *int
}

type rarityPlay struct {
	captured, truncated bool
	files               []rarityFile
}

type rarityTouch struct {
	repo, path string
	at         time.Time
}

type storedRarity struct {
	formula, revealed, predicted string
	score, predictedScore        float64
	percentile                   *float64
	target, quarter              string
	size                         int
	inputs                       CardRarityInputs
	revealedAt                   time.Time
}

type rarityState struct {
	opts    *RarityOptions
	plays   map[int64]*rarityPlay
	seen    map[rarityTouch]bool
	stored  *storedRarity
	cohort  []float64
	quarter string
}

func (c *OperatorCard) loadRarity(ctx context.Context, tx pgx.Tx, id int64, opts *RarityOptions, now time.Time) error {
	st := &rarityState{opts: opts, plays: map[int64]*rarityPlay{}, seen: map[rarityTouch]bool{}, quarter: rarity.Quarter(now)}
	c.rarityState = st
	stored, err := readStoredRarity(ctx, tx, id)
	if err != nil {
		return err
	}
	st.stored = stored
	if stored != nil {
		return nil
	}
	if len(c.Plays) > 0 {
		ids := make([]int64, 0, len(c.Plays))
		for _, p := range c.Plays {
			ids = append(ids, p.id)
		}
		rows, err := tx.Query(ctx, `SELECT id, files_captured_at IS NOT NULL, COALESCE(files_truncated, false)
			FROM pull_requests WHERE id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pid int64
			p := &rarityPlay{}
			if err := rows.Scan(&pid, &p.captured, &p.truncated); err != nil {
				rows.Close()
				return err
			}
			st.plays[pid] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		files, err := tx.Query(ctx, `SELECT pull_request_id, path, additions, deletions FROM pull_request_files
			WHERE pull_request_id = ANY($1) ORDER BY pull_request_id, path LIMIT $2`, ids, cardPlayLimit*MaxStoredFiles)
		if err != nil {
			return err
		}
		for files.Next() {
			var pid int64
			var f rarityFile
			if err := files.Scan(&pid, &f.path, &f.additions, &f.deletes); err != nil {
				files.Close()
				return err
			}
			if p := st.plays[pid]; p != nil {
				p.files = append(p.files, f)
			}
		}
		files.Close()
		if err := files.Err(); err != nil {
			return err
		}
		if err := c.loadNovelty(ctx, tx, id); err != nil {
			return err
		}
	}
	if target := c.rarityTarget(); target != "" {
		st.cohort, err = rarityCohort(ctx, tx, rarity.Formula, target, st.quarter, id)
		if err != nil {
			return err
		}
	}
	return nil
}

func readStoredRarity(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64) (*storedRarity, error) {
	var s storedRarity
	var inputs []byte
	err := q.QueryRow(ctx, `SELECT formula, revealed_tier, predicted_tier, score::float8, predicted_score::float8, percentile::float8,
		cohort_target, cohort_quarter, cohort_size, inputs, revealed_at
		FROM card_rarity WHERE work_item_id = $1 AND revealed_tier IS NOT NULL`, id).
		Scan(&s.formula, &s.revealed, &s.predicted, &s.score, &s.predictedScore, &s.percentile,
			&s.target, &s.quarter, &s.size, &inputs, &s.revealedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(inputs, &s.inputs); err != nil {
		return nil, err
	}
	s.revealedAt = s.revealedAt.UTC()
	return &s, nil
}

func rarityCohort(ctx context.Context, tx pgx.Tx, formula, target, quarter string, id int64) ([]float64, error) {
	rows, err := tx.Query(ctx, `SELECT score::float8 FROM card_rarity
		WHERE formula = $1 AND cohort_target = $2 AND cohort_quarter = $3 AND revealed_tier IS NOT NULL AND work_item_id <> $4`,
		formula, target, quarter, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scores := []float64{}
	for rows.Next() {
		var s float64
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		scores = append(scores, s)
	}
	return scores, rows.Err()
}

func (c *OperatorCard) touches(plays []CardPlay) []rarityTouch {
	first := map[[2]string]time.Time{}
	for _, p := range plays {
		rp := c.rarityState.plays[p.id]
		if rp == nil || !rp.captured || p.MergedAt == nil {
			continue
		}
		m := c.rarityState.opts.matcher(p.repo)
		for _, f := range rp.files {
			if m.Excluded(f.path) {
				continue
			}
			key := [2]string{strings.ToLower(p.repo), f.path}
			if at, ok := first[key]; !ok || p.MergedAt.Before(at) {
				first[key] = *p.MergedAt
			}
		}
	}
	out := make([]rarityTouch, 0, len(first))
	for key, at := range first {
		out = append(out, rarityTouch{repo: key[0], path: key[1], at: at.UTC()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].repo != out[j].repo {
			return out[i].repo < out[j].repo
		}
		return out[i].path < out[j].path
	})
	return out
}

func (c *OperatorCard) loadNovelty(ctx context.Context, tx pgx.Tx, id int64) error {
	set := map[rarityTouch]bool{}
	for _, t := range append(c.touches(c.mergedPlays()), c.touches(c.previousPlays())...) {
		set[t] = true
	}
	if len(set) == 0 {
		return nil
	}
	var repos, paths []string
	var ats []time.Time
	for t := range set {
		repos, paths, ats = append(repos, t.repo), append(paths, t.path), append(ats, t.at)
	}
	rows, err := tx.Query(ctx, `SELECT t.repo, t.path, t.at FROM unnest($1::text[], $2::text[], $3::timestamptz[]) AS t(repo, path, at)
		WHERE EXISTS (SELECT 1 FROM pull_request_files f JOIN pull_requests p ON p.id = f.pull_request_id
			WHERE f.path = t.path AND lower(p.repo_owner || '/' || p.repo_name) = t.repo AND p.state = 'merged'
				AND p.merged_at < t.at AND p.merged_at >= t.at - make_interval(days => $4) AND p.work_item_id IS DISTINCT FROM $5)`,
		repos, paths, ats, int(rarity.NoveltyWindow/(24*time.Hour)), id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t rarityTouch
		if err := rows.Scan(&t.repo, &t.path, &t.at); err != nil {
			return err
		}
		t.at = t.at.UTC()
		c.rarityState.seen[t] = true
	}
	return rows.Err()
}

func (c *OperatorCard) mergedPlays() []CardPlay {
	var out []CardPlay
	for _, p := range c.Plays {
		if p.State == "merged" {
			out = append(out, p)
		}
	}
	return out
}

func (c *OperatorCard) previousPlays() []CardPlay {
	if len(c.Plays) < 2 {
		return nil
	}
	var out []CardPlay
	for _, p := range c.Plays[:len(c.Plays)-1] {
		if p.State == "merged" {
			out = append(out, p)
		}
	}
	return out
}

func (c *OperatorCard) rarityTarget() string {
	if c.Target != nil {
		return strings.ToLower(c.Target.Owner + "/" + c.Target.Repo)
	}
	if p := c.latestMerged(); p != nil {
		return strings.ToLower(p.repo)
	}
	if len(c.Plays) > 0 {
		return strings.ToLower(c.Plays[len(c.Plays)-1].repo)
	}
	return ""
}

func playLines(p CardPlay) *int64 {
	if p.Additions == nil || p.Deletions == nil {
		return nil
	}
	n := int64(*p.Additions + *p.Deletions)
	return &n
}

func countedLines(p CardPlay, rp *rarityPlay, m rarity.Matcher) *int64 {
	if rp == nil || !rp.captured {
		return nil
	}
	files := make([]rarity.FileLines, 0, len(rp.files))
	for _, f := range rp.files {
		files = append(files, rarity.FileLines{Path: f.path, Additions: f.additions, Deletions: f.deletes})
	}
	return m.CountedLines(files, playLines(p), rp.truncated)
}

type rarityFacts struct {
	facts  rarity.Facts
	inputs CardRarityInputs
}

func (c *OperatorCard) fileFacts(plays []CardPlay, opts *RarityOptions, in *CardRarityInputs, f *rarity.Facts) {
	touched := c.touches(plays)
	if len(touched) == 0 {
		return
	}
	modules := map[string]bool{}
	var sensitive []string
	novel := 0
	for _, t := range touched {
		modules[t.repo+"\x00"+rarity.Module(t.path)] = true
		if opts.matcher(t.repo).Sensitive(t.path) {
			sensitive = append(sensitive, t.path)
		}
		if !c.rarityState.seen[t] {
			novel++
		}
	}
	nModules, nSensitive, nFiles := len(modules), len(sensitive), len(touched)
	share := float64(novel) / float64(nFiles)
	f.Modules, f.SensitiveFiles, f.Files, f.NovelFiles = &nModules, &nSensitive, &nFiles, &novel
	in.Reach.Modules = &nModules
	in.Sensitive.Files = &nSensitive
	paths := rarity.SortedUnique(sensitive)
	if len(paths) > cardRarityPaths {
		paths = paths[:cardRarityPaths]
	}
	in.Sensitive.Paths = paths
	in.Novelty = CardRarityNovelty{Share: &share, Files: &nFiles, Novel: &novel}
	for _, p := range plays {
		if rp := c.rarityState.plays[p.id]; rp != nil && rp.truncated {
			in.Truncated = true
		}
	}
}

func distinctRepos(plays []CardPlay) *int {
	if len(plays) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, p := range plays {
		set[strings.ToLower(p.repo)] = true
	}
	n := len(set)
	return &n
}

func newRarityInputs() CardRarityInputs {
	return CardRarityInputs{Sensitive: CardRaritySensitive{Paths: []string{}}, NotCollected: append([]string{}, rarity.NotCollected...)}
}

func (c *OperatorCard) revealedFacts(opts *RarityOptions) (rarityFacts, bool) {
	merged := c.mergedPlays()
	if c.Release == nil || c.State != "merged" || len(merged) == 0 {
		return rarityFacts{}, false
	}
	for _, p := range merged {
		if rp := c.rarityState.plays[p.id]; rp == nil || !rp.captured {
			return rarityFacts{}, false
		}
	}
	r := rarityFacts{inputs: newRarityInputs()}
	c.fileFacts(merged, opts, &r.inputs, &r.facts)
	r.facts.Repos = distinctRepos(merged)
	r.inputs.Reach.Repos = r.facts.Repos
	var lines int64
	known := true
	for _, p := range merged {
		n := countedLines(p, c.rarityState.plays[p.id], opts.matcher(p.repo))
		if n == nil {
			known = false
			break
		}
		lines += *n
	}
	if known {
		r.facts.CountedLines = &lines
		r.inputs.Size.CountedLines = &lines
	}
	return r, true
}

func (c *OperatorCard) predictedFacts(opts *RarityOptions) rarityFacts {
	r := rarityFacts{inputs: newRarityInputs()}
	previous := c.previousPlays()
	c.fileFacts(previous, opts, &r.inputs, &r.facts)
	switch {
	case len(c.Plays) > 0:
		r.facts.Repos = distinctRepos(c.Plays)
	case c.Target != nil:
		one := 1
		r.facts.Repos = &one
	}
	r.inputs.Reach.Repos = r.facts.Repos
	if len(c.Plays) > 0 {
		var lines int64
		known := true
		for _, p := range previous {
			n := countedLines(p, c.rarityState.plays[p.id], opts.matcher(p.repo))
			if n == nil {
				known = false
				break
			}
			lines += *n
		}
		if current := playLines(c.Plays[len(c.Plays)-1]); current != nil {
			lines += *current
		} else if len(previous) == 0 {
			known = false
		}
		if known {
			r.facts.CountedLines = &lines
			r.inputs.Size.CountedLines = &lines
		}
	}
	set := c.Set != nil
	r.facts.Set = set
	r.inputs.Set = &set
	return r
}

type rarityReveal struct {
	target, quarter       string
	score, predictedScore float64
	inputs                CardRarityInputs
	revealedAt            time.Time
}

func (c *OperatorCard) rarity(opts *RarityOptions) (*CardRarity, *rarityReveal) {
	st := c.rarityState
	if st == nil {
		return nil, nil
	}
	setComplete := c.Set != nil && c.Set.Role == "epic" && c.Set.Complete
	if s := st.stored; s != nil {
		r := &CardRarity{Formula: s.formula, Predicted: &s.predicted, Revealed: &s.revealed, Tier: s.revealed,
			Percentile: s.percentile, Inputs: s.inputs, Cohort: &CardRarityCohort{Target: s.target, Quarter: s.quarter, Size: s.size}}
		score, at := s.score, s.revealedAt
		r.Score, r.RevealedAt = &score, &at
		if setComplete {
			r.Tier = rarity.Legendary
		}
		return r, nil
	}
	target := c.rarityTarget()
	minted := len(c.started()) > 0
	if revealed, ok := c.revealedFacts(opts); ok && target != "" {
		predicted := c.predictedFacts(opts)
		predictedScore, _ := rarity.Score(predicted.facts, true)
		score, _ := rarity.Score(revealed.facts, false)
		return nil, &rarityReveal{target: target, quarter: rarity.Quarter(c.Release.At), score: score,
			predictedScore: predictedScore, inputs: revealed.inputs, revealedAt: c.Release.At.UTC()}
	}
	if !minted && !setComplete {
		return nil, nil
	}
	r := &CardRarity{Formula: rarity.Formula, Inputs: newRarityInputs()}
	if minted {
		predicted := c.predictedFacts(opts)
		score, _ := rarity.Score(predicted.facts, true)
		tier, percentile, size := rarity.Tier(score, st.cohort)
		r.Predicted, r.Tier, r.Score, r.Percentile, r.Inputs = &tier, tier, &score, percentile, predicted.inputs
		if target != "" {
			r.Cohort = &CardRarityCohort{Target: target, Quarter: st.quarter, Size: size}
		}
	}
	if setComplete {
		r.Tier = rarity.Legendary
	}
	return r, nil
}

func (s *Store) cardRarity(ctx context.Context, c *OperatorCard, id int64, opts CardOptions) (*CardRarity, error) {
	r, reveal := c.rarity(opts.Rarity)
	if reveal == nil {
		return r, nil
	}
	stored, err := s.freezeRarity(ctx, id, reveal, cardNow(opts))
	if err != nil || stored == nil {
		return nil, err
	}
	c.rarityState.stored = stored
	r, _ = c.rarity(opts.Rarity)
	return r, nil
}

func (s *Store) freezeRarity(ctx context.Context, id int64, r *rarityReveal, now time.Time) (*storedRarity, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ploeg.card-rarity:' || $1 || ':' || $2 || ':' || $3, 0))`,
		rarity.Formula, r.target, r.quarter); err != nil {
		return nil, err
	}
	stored, err := readStoredRarity(ctx, tx, id)
	if err != nil || stored != nil {
		return stored, err
	}
	cohort, err := rarityCohort(ctx, tx, rarity.Formula, r.target, r.quarter, id)
	if err != nil {
		return nil, err
	}
	tier, percentile, size := rarity.Tier(r.score, cohort)
	predicted, _, _ := rarity.Tier(r.predictedScore, cohort)
	inputs, err := json.Marshal(r.inputs)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO card_rarity (work_item_id, checked_at, formula, revealed_tier, predicted_tier, score,
			predicted_score, percentile, cohort_target, cohort_quarter, cohort_size, inputs, revealed_at, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $2)
		ON CONFLICT (work_item_id) DO UPDATE SET checked_at = EXCLUDED.checked_at, formula = EXCLUDED.formula,
			revealed_tier = EXCLUDED.revealed_tier, predicted_tier = EXCLUDED.predicted_tier, score = EXCLUDED.score,
			predicted_score = EXCLUDED.predicted_score, percentile = EXCLUDED.percentile, cohort_target = EXCLUDED.cohort_target,
			cohort_quarter = EXCLUDED.cohort_quarter, cohort_size = EXCLUDED.cohort_size, inputs = EXCLUDED.inputs,
			revealed_at = EXCLUDED.revealed_at, recorded_at = EXCLUDED.recorded_at
		WHERE card_rarity.revealed_tier IS NULL`,
		id, now, rarity.Formula, tier, predicted, r.score, r.predictedScore, percentile, r.target, r.quarter, size, inputs,
		r.revealedAt); err != nil {
		return nil, err
	}
	stored, err = readStoredRarity(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return stored, tx.Commit(ctx)
}

// RarityCandidates returns up to limit Work Items whose rarity is not
// revealed yet, every merged play of which has its files recorded, and that
// were not checked since checkedBefore, the least recently checked first.
func (s *Store) RarityCandidates(ctx context.Context, checkedBefore time.Time, limit int) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.id FROM work_items i LEFT JOIN card_rarity r ON r.work_item_id = i.id
		WHERE (r.work_item_id IS NULL OR (r.revealed_tier IS NULL AND r.checked_at < $1))
			AND EXISTS (SELECT 1 FROM pull_requests p WHERE p.work_item_id = i.id AND p.state = 'merged')
			AND NOT EXISTS (SELECT 1 FROM pull_requests p WHERE p.work_item_id = i.id AND p.state = 'merged' AND p.files_captured_at IS NULL)
		ORDER BY r.checked_at NULLS FIRST, i.id LIMIT $2`, checkedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkRarityChecked records that the rarity sweep looked at Work Item id at
// at and found nothing to reveal.
func (s *Store) MarkRarityChecked(ctx context.Context, id int64, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO card_rarity (work_item_id, checked_at) VALUES ($1, $2)
		ON CONFLICT (work_item_id) DO UPDATE SET checked_at = EXCLUDED.checked_at WHERE card_rarity.revealed_tier IS NULL`, id, at)
	return err
}
