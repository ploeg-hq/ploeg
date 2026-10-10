package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ErrAskNotFound reports an Ask that does not exist for the consumer and actor.
var ErrAskNotFound = errors.New("ask not found")

// ErrAskConflict reports an askId reused for another Work Item, question or
// actor, or a Work Item whose team changed under the admission.
var ErrAskConflict = errors.New("ask conflicts with an earlier ask of the same id")

// ErrAllowanceExhausted reports an allowance that cannot cover the per-Ask
// Budget. The error is an *AllowanceExhaustedError carrying the allowance.
var ErrAllowanceExhausted = errors.New("allowance cannot cover the per-Ask Budget")

// AllowancePurposeAsk and AllowanceScopeTeam are the purpose and scope kind
// phase 1 of ADR-0082 uses.
const (
	AllowancePurposeAsk = "ask"
	AllowanceScopeTeam  = "team"
)

// AllowanceExhaustedError carries the allowance an Ask was refused against.
type AllowanceExhaustedError struct{ Allowance Allowance }

func (e *AllowanceExhaustedError) Error() string { return ErrAllowanceExhausted.Error() }
func (e *AllowanceExhaustedError) Unwrap() error { return ErrAllowanceExhausted }

// Allowance is one scope's allowance for one period. SettledUSD is the
// reconciled spend of the period's Asks, HeldUSD what their unsettled
// accounts still hold, and RemainingUSD the limit less both, never below
// zero. ResetAt is the end of the period.
type Allowance struct {
	Purpose      string    `json:"purpose"`
	ScopeKind    string    `json:"scopeKind"`
	ScopeID      string    `json:"scopeId"`
	PeriodStart  time.Time `json:"periodStart"`
	ResetAt      time.Time `json:"resetAt"`
	LimitUSD     float64   `json:"limitUsd"`
	SettledUSD   float64   `json:"settledUsd"`
	HeldUSD      float64   `json:"heldUsd"`
	RemainingUSD float64   `json:"remainingUsd"`
	AskCount     int64     `json:"askCount"`
}

// AskSpend is what an Ask has cost so far. CostStatus is provisional until
// its account settles, settled after, and unknown when the settlement found
// no spend-log entries. KeyState is the inference account's state, or none
// when no account was reserved.
type AskSpend struct {
	CostStatus string   `json:"costStatus"`
	USD        *float64 `json:"usd"`
	KeyState   string   `json:"keyState"`
}

// Ask is a read-only Run about a Work Item, outside any Shift (ADR-0082).
// State is open while its Run runs before its deadline, finished once the
// consumer finished it, and expired when its deadline passed first.
type Ask struct {
	AskID          string     `json:"askId"`
	RunID          string     `json:"runId"`
	WorkItemID     string     `json:"workItemId"`
	Team           string     `json:"team"`
	Actor          string     `json:"actor"`
	AskedBy        string     `json:"askedBy"`
	State          string     `json:"state"`
	BudgetUSD      float64    `json:"budgetUsd"`
	QuestionSHA256 string     `json:"questionSha256"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
	Spend          AskSpend   `json:"spend"`
	ScopeKind      string     `json:"-"`
	ScopeID        string     `json:"-"`
	PeriodStart    time.Time  `json:"-"`
	RunToken       string     `json:"-"`
}

// AdmitAsk is one Ask admission. BudgetUSD is the per-Ask Budget, LimitUSD
// the limit a period's row is created with, TTL the Run's deadline and the
// key's lifetime, and At the time that picks the period (zero means now).
type AdmitAsk struct {
	AskID          string
	WorkItemID     int64
	Team           string
	QuestionSHA256 string
	AskedBy        string
	BudgetUSD      float64
	LimitUSD       float64
	TTL            time.Duration
	At             time.Time
}

var questionDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MonthPeriod is the UTC calendar month holding at.
func MonthPeriod(at time.Time) (time.Time, time.Time) {
	at = at.UTC()
	start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

const askSelect = `SELECT k.ask_id, r.id::text, k.work_item_id::text, r.team, k.actor, k.asked_by,
	COALESCE(a.authorized, r.authorized)::float8, k.question_sha256, k.created_at, r.expires_at, k.finished_at,
	CASE WHEN k.finished_at IS NOT NULL THEN 'finished'
	     WHEN r.state = 'running' AND r.expires_at > now() THEN 'open' ELSE 'expired' END,
	COALESCE(a.state, 'none'), a.observed_spend::float8, a.reconciled_spend::float8,
	COALESCE(a.cost_known, true), COALESCE(a.corrections_until > now(), false),
	al.scope_kind, al.scope_id, al.period_start, r.run_token
FROM asks k JOIN agent_runs r ON r.id = k.run_id JOIN inference_allowances al ON al.id = k.allowance_id
LEFT JOIN run_llm_accounts a ON a.run_token = r.run_token `

func scanAsk(row pgx.Row) (Ask, error) {
	var a Ask
	var observed, reconciled *float64
	var known, correcting bool
	err := row.Scan(&a.AskID, &a.RunID, &a.WorkItemID, &a.Team, &a.Actor, &a.AskedBy, &a.BudgetUSD, &a.QuestionSHA256,
		&a.CreatedAt, &a.ExpiresAt, &a.FinishedAt, &a.State, &a.Spend.KeyState, &observed, &reconciled, &known, &correcting,
		&a.ScopeKind, &a.ScopeID, &a.PeriodStart, &a.RunToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrAskNotFound
	}
	if err != nil {
		return a, err
	}
	switch {
	case a.Spend.KeyState == "reconciled" && !known:
		a.Spend.CostStatus, a.Spend.USD = "unknown", reconciled
	case a.Spend.KeyState == "reconciled" && !correcting:
		a.Spend.CostStatus, a.Spend.USD = "settled", reconciled
	case a.Spend.KeyState == "reconciled":
		a.Spend.CostStatus, a.Spend.USD = "provisional", reconciled
	default:
		a.Spend.CostStatus, a.Spend.USD = "provisional", observed
	}
	return a, nil
}

func askFingerprint(in AdmitAsk) string {
	return executionFingerprint(map[string]any{"workItemId": in.WorkItemID, "questionSha256": in.QuestionSHA256, "askedBy": in.AskedBy})
}

// AskWorkItemTeam returns the team of a Work Item in teams (nil means any),
// or ErrWorkItemNotFound.
func (s *Store) AskWorkItemTeam(ctx context.Context, workItemID int64, teams []string) (string, error) {
	var team string
	err := s.pool.QueryRow(ctx, `SELECT team FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2))`,
		workItemID, teams).Scan(&team)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrWorkItemNotFound
	}
	return team, err
}

// AdmitAsk admits an Ask on an existing Work Item against its team's
// allowance for the period, or returns the Ask an earlier admission with the
// same consumer and askId created. It creates no Shift and no Lease. The
// period's allowance row is locked for the whole admission, so concurrent
// admissions see each other's holds (ADR-0012, ADR-0082).
func (s *Store) AdmitAsk(ctx context.Context, consumer, actor string, in AdmitAsk) (Ask, bool, error) {
	if in.AskID == "" || !questionDigest.MatchString(in.QuestionSHA256) || !validSpend(in.BudgetUSD) || in.BudgetUSD <= 0 ||
		!validSpend(in.LimitUSD) || in.TTL < time.Second {
		return Ask{}, false, fmt.Errorf("invalid ask admission")
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Ask{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "ploeg.ask:"+consumer+":"+in.AskID); err != nil {
		return Ask{}, false, err
	}
	fingerprint := askFingerprint(in)
	var prior, priorActor string
	err = tx.QueryRow(ctx, `SELECT fingerprint, actor FROM asks WHERE consumer = $1 AND ask_id = $2`, consumer, in.AskID).Scan(&prior, &priorActor)
	if err == nil {
		if prior != fingerprint || priorActor != actor {
			return Ask{}, false, ErrAskConflict
		}
		existing, err := scanAsk(tx.QueryRow(ctx, askSelect+`WHERE k.consumer = $1 AND k.ask_id = $2`, consumer, in.AskID))
		if err != nil {
			return existing, false, err
		}
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Ask{}, false, err
	}
	var team string
	err = tx.QueryRow(ctx, `SELECT team FROM work_items WHERE id = $1 FOR SHARE`, in.WorkItemID).Scan(&team)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ask{}, false, ErrWorkItemNotFound
	}
	if err != nil {
		return Ask{}, false, err
	}
	if team != in.Team {
		return Ask{}, false, ErrAskConflict
	}
	start, end := MonthPeriod(at)
	if _, err = tx.Exec(ctx, `INSERT INTO inference_allowances (purpose, scope_kind, scope_id, period_start, period_end, limit_usd)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (purpose, scope_kind, scope_id, period_start) DO NOTHING`,
		AllowancePurposeAsk, AllowanceScopeTeam, team, start, end, in.LimitUSD); err != nil {
		return Ask{}, false, err
	}
	var allowanceID int64
	if err = tx.QueryRow(ctx, `SELECT id FROM inference_allowances
		WHERE purpose = $1 AND scope_kind = $2 AND scope_id = $3 AND period_start = $4 FOR UPDATE`,
		AllowancePurposeAsk, AllowanceScopeTeam, team, start).Scan(&allowanceID); err != nil {
		return Ask{}, false, err
	}
	state, covers, err := allowanceByID(ctx, tx, allowanceID, in.BudgetUSD)
	if err != nil {
		return Ask{}, false, err
	}
	if !covers {
		return Ask{}, false, &AllowanceExhaustedError{Allowance: state}
	}
	token, err := newToken()
	if err != nil {
		return Ask{}, false, err
	}
	var runID int64
	if err = tx.QueryRow(ctx, `INSERT INTO agent_runs (work_item_id, team, run_token, role, round, writes, state, authorized, started_at, expires_at)
		VALUES ($1, $2, $3, $4, 0, false, 'running', $5, now(), now() + make_interval(secs => $6)) RETURNING id`,
		in.WorkItemID, team, token, work.AskRole, in.BudgetUSD, in.TTL.Seconds()).Scan(&runID); err != nil {
		return Ask{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO asks (run_id, consumer, ask_id, actor, asked_by, work_item_id, allowance_id, question_sha256, fingerprint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		runID, consumer, in.AskID, actor, in.AskedBy, in.WorkItemID, allowanceID, in.QuestionSHA256, fingerprint); err != nil {
		return Ask{}, false, err
	}
	workItemID := in.WorkItemID
	if err = audit(ctx, tx, "operator:"+consumer+":"+actor, "ask.admitted", &workItemID, map[string]any{
		"askId": in.AskID, "run": runID, "budgetUsd": in.BudgetUSD, "periodStart": start}); err != nil {
		return Ask{}, false, err
	}
	admitted, err := scanAsk(tx.QueryRow(ctx, askSelect+`WHERE k.run_id = $1`, runID))
	if err != nil {
		return admitted, false, err
	}
	return admitted, true, tx.Commit(ctx)
}

func allowanceByID(ctx context.Context, q pgx.Tx, id int64, budget float64) (Allowance, bool, error) {
	var a Allowance
	var covers bool
	err := q.QueryRow(ctx, `WITH spend AS (
			SELECT COALESCE(SUM(CASE WHEN la.state = 'reconciled' THEN la.reconciled_spend ELSE 0 END), 0) AS settled,
			       COALESCE(SUM(h.reserved), 0) AS held, count(*) AS asks
			FROM asks k JOIN agent_runs r ON r.id = k.run_id
			JOIN run_budget_holds h ON h.run_token = r.run_token
			LEFT JOIN run_llm_accounts la ON la.run_token = r.run_token
			WHERE k.allowance_id = $1)
		SELECT al.purpose, al.scope_kind, al.scope_id, al.period_start, al.period_end, al.limit_usd::float8,
		       s.settled::float8, s.held::float8, GREATEST(al.limit_usd - s.settled - s.held, 0)::float8, s.asks,
		       al.limit_usd - s.settled - s.held >= $2::numeric
		FROM inference_allowances al, spend s WHERE al.id = $1`, id, budget).
		Scan(&a.Purpose, &a.ScopeKind, &a.ScopeID, &a.PeriodStart, &a.ResetAt, &a.LimitUSD, &a.SettledUSD, &a.HeldUSD, &a.RemainingUSD, &a.AskCount, &covers)
	return a, covers, err
}

// Ask reads one Ask of a consumer and actor.
func (s *Store) Ask(ctx context.Context, consumer, actor, askID string) (Ask, error) {
	return scanAsk(s.pool.QueryRow(ctx, askSelect+`WHERE k.consumer = $1 AND k.ask_id = $2 AND k.actor = $3`, consumer, askID, actor))
}

// FinishAsk records that the consumer finished an Ask and finishes its Run,
// so the caller can block its key and the settlement sweep can settle it.
// Finishing again changes nothing. An Ask whose deadline already finished
// its Run is still recorded as finished by the consumer.
func (s *Store) FinishAsk(ctx context.Context, consumer, actor, askID string) (Ask, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Ask{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var runID, workItemID int64
	var finished *time.Time
	err = tx.QueryRow(ctx, `SELECT run_id, work_item_id, finished_at FROM asks
		WHERE consumer = $1 AND ask_id = $2 AND actor = $3 FOR UPDATE`, consumer, askID, actor).Scan(&runID, &workItemID, &finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ask{}, ErrAskNotFound
	}
	if err != nil {
		return Ask{}, err
	}
	if finished == nil {
		if _, err = tx.Exec(ctx, `UPDATE asks SET finished_at = now() WHERE run_id = $1`, runID); err != nil {
			return Ask{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now(), summary = 'Ask finished'
			WHERE id = $1 AND state = 'running'`, runID); err != nil {
			return Ask{}, err
		}
		if err = audit(ctx, tx, "operator:"+consumer+":"+actor, "ask.finished", &workItemID, map[string]any{
			"askId": askID, "run": runID}); err != nil {
			return Ask{}, err
		}
	}
	a, err := scanAsk(tx.QueryRow(ctx, askSelect+`WHERE k.run_id = $1`, runID))
	if err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}
