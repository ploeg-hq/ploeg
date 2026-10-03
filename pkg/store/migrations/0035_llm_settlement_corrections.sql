-- VIK-1755: a settled account keeps its first settlement, is revisited until
-- corrections_until, and records each later positive correction as an
-- adjustment. cost_known is false when the gateway had no spend-log entry
-- for a minted key, so the zero it settled at is not a confirmed cost.
ALTER TABLE run_llm_accounts
    ADD COLUMN settled_at TIMESTAMPTZ,
    ADD COLUMN settled_spend NUMERIC(12, 4) CHECK (settled_spend IS NULL OR settled_spend >= 0),
    ADD COLUMN corrections_until TIMESTAMPTZ,
    ADD COLUMN cost_known BOOLEAN NOT NULL DEFAULT true;

UPDATE run_llm_accounts
SET settled_at = updated_at,
    settled_spend = reconciled_spend,
    cost_known = reconciliation_evidence NOT LIKE 'litellm:spend-logs % entries=0 %'
WHERE state = 'reconciled';

CREATE INDEX run_llm_accounts_correctable ON run_llm_accounts (corrections_until)
    WHERE state = 'reconciled' AND corrections_until IS NOT NULL;

CREATE TABLE run_llm_adjustments (
    id BIGSERIAL PRIMARY KEY,
    run_token TEXT NOT NULL REFERENCES run_llm_accounts(run_token) ON DELETE CASCADE,
    previous_spend NUMERIC(12, 4) NOT NULL CHECK (previous_spend >= 0),
    spend NUMERIC(12, 4) NOT NULL CHECK (spend > previous_spend),
    evidence TEXT NOT NULL CHECK (evidence <> ''),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX run_llm_adjustments_by_account ON run_llm_adjustments (run_token, id);
