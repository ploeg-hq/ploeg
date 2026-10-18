package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ErrChatNotFound reports a chat message that does not exist for the
// consumer and actor.
var ErrChatNotFound = errors.New("chat not found")

// ErrChatConflict reports a chatId reused for another Team, message or actor.
var ErrChatConflict = errors.New("chat conflicts with an earlier chat of the same id")

// ChatSpend is what a chat message has cost so far, with the same states as
// an Ask's spend.
type ChatSpend = AskSpend

// Chat is one chat message: a read-only Run about no Work Item, outside any
// Shift, paid from its Team's Chat Allowance (ADR-0128). State is open while
// its Run runs before its deadline, finished once the consumer finished it,
// and expired when its deadline passed first.
type Chat struct {
	ChatID        string     `json:"chatId"`
	RunID         string     `json:"runId"`
	Team          string     `json:"team"`
	Actor         string     `json:"actor"`
	AskedBy       string     `json:"askedBy"`
	State         string     `json:"state"`
	BudgetUSD     float64    `json:"budgetUsd"`
	MessageSHA256 string     `json:"messageSha256"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	Spend         ChatSpend  `json:"spend"`
	ScopeKind     string     `json:"-"`
	ScopeID       string     `json:"-"`
	PeriodStart   time.Time  `json:"-"`
	RunToken      string     `json:"-"`
}

// AdmitChat is one chat admission. BudgetUSD is the per-chat Budget, LimitUSD
// the limit a period's row is created with, TTL the Run's deadline and the
// key's lifetime, and At the time that picks the period (zero means now).
type AdmitChat struct {
	ChatID        string
	Team          string
	MessageSHA256 string
	AskedBy       string
	BudgetUSD     float64
	LimitUSD      float64
	TTL           time.Duration
	At            time.Time
}

const chatSelect = `SELECT c.chat_id, r.id::text, c.team, c.actor, c.asked_by,
	COALESCE(a.authorized, r.authorized)::float8, c.message_sha256, c.created_at, r.expires_at, c.finished_at,
	CASE WHEN c.finished_at IS NOT NULL THEN 'finished'
	     WHEN r.state = 'running' AND r.expires_at > now() THEN 'open' ELSE 'expired' END,
	COALESCE(a.state, 'none'), a.observed_spend::float8, a.reconciled_spend::float8,
	COALESCE(a.cost_known, true), COALESCE(a.corrections_until > now(), false),
	al.scope_kind, al.scope_id, al.period_start, r.run_token
FROM chats c JOIN agent_runs r ON r.id = c.run_id JOIN inference_allowances al ON al.id = c.allowance_id
LEFT JOIN run_llm_accounts a ON a.run_token = r.run_token `

func scanChat(row pgx.Row) (Chat, error) {
	var c Chat
	var observed, reconciled *float64
	var known, correcting bool
	err := row.Scan(&c.ChatID, &c.RunID, &c.Team, &c.Actor, &c.AskedBy, &c.BudgetUSD, &c.MessageSHA256,
		&c.CreatedAt, &c.ExpiresAt, &c.FinishedAt, &c.State, &c.Spend.KeyState, &observed, &reconciled, &known, &correcting,
		&c.ScopeKind, &c.ScopeID, &c.PeriodStart, &c.RunToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrChatNotFound
	}
	if err != nil {
		return c, err
	}
	c.Spend.costFrom(observed, reconciled, known, correcting)
	return c, nil
}

func chatFingerprint(in AdmitChat) string {
	return executionFingerprint(map[string]any{"team": in.Team, "messageSha256": in.MessageSHA256, "askedBy": in.AskedBy})
}

// AdmitChat admits a chat message for a Team against the Team's Chat
// Allowance for the period, or returns the chat an earlier admission with the
// same consumer and chatId created. It creates no Shift, no Lease and no
// Work Item. The period's allowance row is locked for the whole admission, so
// concurrent admissions see each other's holds (ADR-0012, ADR-0128).
func (s *Store) AdmitChat(ctx context.Context, consumer, actor string, in AdmitChat) (Chat, bool, error) {
	if in.ChatID == "" || in.Team == "" || !questionDigest.MatchString(in.MessageSHA256) || !validSpend(in.BudgetUSD) || in.BudgetUSD <= 0 ||
		!validSpend(in.LimitUSD) || in.TTL < time.Second {
		return Chat{}, false, fmt.Errorf("invalid chat admission")
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Chat{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "ploeg.chat:"+consumer+":"+in.ChatID); err != nil {
		return Chat{}, false, err
	}
	fingerprint := chatFingerprint(in)
	var prior, priorActor string
	err = tx.QueryRow(ctx, `SELECT fingerprint, actor FROM chats WHERE consumer = $1 AND chat_id = $2`, consumer, in.ChatID).Scan(&prior, &priorActor)
	if err == nil {
		if prior != fingerprint || priorActor != actor {
			return Chat{}, false, ErrChatConflict
		}
		existing, err := scanChat(tx.QueryRow(ctx, chatSelect+`WHERE c.consumer = $1 AND c.chat_id = $2`, consumer, in.ChatID))
		if err != nil {
			return existing, false, err
		}
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, false, err
	}
	start, _ := MonthPeriod(at)
	allowanceID, err := lockTeamAllowance(ctx, tx, AllowancePurposeChat, in.Team, at, in.LimitUSD, in.BudgetUSD)
	if err != nil {
		return Chat{}, false, err
	}
	token, err := newToken()
	if err != nil {
		return Chat{}, false, err
	}
	var runID int64
	if err = tx.QueryRow(ctx, `INSERT INTO agent_runs (work_item_id, team, run_token, role, round, writes, state, authorized, started_at, expires_at)
		VALUES (NULL, $1, $2, $3, 0, false, 'running', $4, now(), now() + make_interval(secs => $5)) RETURNING id`,
		in.Team, token, work.ChatRole, in.BudgetUSD, in.TTL.Seconds()).Scan(&runID); err != nil {
		return Chat{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chats (run_id, consumer, chat_id, actor, asked_by, team, allowance_id, message_sha256, fingerprint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		runID, consumer, in.ChatID, actor, in.AskedBy, in.Team, allowanceID, in.MessageSHA256, fingerprint); err != nil {
		return Chat{}, false, err
	}
	if err = audit(ctx, tx, "operator:"+consumer+":"+actor, "chat.admitted", nil, map[string]any{
		"chatId": in.ChatID, "team": in.Team, "run": runID, "budgetUsd": in.BudgetUSD, "periodStart": start}); err != nil {
		return Chat{}, false, err
	}
	admitted, err := scanChat(tx.QueryRow(ctx, chatSelect+`WHERE c.run_id = $1`, runID))
	if err != nil {
		return admitted, false, err
	}
	return admitted, true, tx.Commit(ctx)
}

// Chat reads one chat message of a consumer and actor.
func (s *Store) Chat(ctx context.Context, consumer, actor, chatID string) (Chat, error) {
	return scanChat(s.pool.QueryRow(ctx, chatSelect+`WHERE c.consumer = $1 AND c.chat_id = $2 AND c.actor = $3`, consumer, chatID, actor))
}

// FinishChat records that the consumer finished a chat message and finishes
// its Run, so the caller can block its key and the settlement sweep can
// settle it. Finishing again changes nothing. A chat whose deadline already
// finished its Run is still recorded as finished by the consumer.
func (s *Store) FinishChat(ctx context.Context, consumer, actor, chatID string) (Chat, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Chat{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var runID int64
	var team string
	var finished *time.Time
	err = tx.QueryRow(ctx, `SELECT run_id, team, finished_at FROM chats
		WHERE consumer = $1 AND chat_id = $2 AND actor = $3 FOR UPDATE`, consumer, chatID, actor).Scan(&runID, &team, &finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, ErrChatNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	if finished == nil {
		if _, err = tx.Exec(ctx, `UPDATE chats SET finished_at = now() WHERE run_id = $1`, runID); err != nil {
			return Chat{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now(), summary = 'Chat finished'
			WHERE id = $1 AND state = 'running'`, runID); err != nil {
			return Chat{}, err
		}
		if err = audit(ctx, tx, "operator:"+consumer+":"+actor, "chat.finished", nil, map[string]any{
			"chatId": chatID, "team": team, "run": runID}); err != nil {
			return Chat{}, err
		}
	}
	c, err := scanChat(tx.QueryRow(ctx, chatSelect+`WHERE c.run_id = $1`, runID))
	if err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
