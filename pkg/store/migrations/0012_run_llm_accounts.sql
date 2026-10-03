CREATE TABLE run_llm_accounts (
    run_token TEXT PRIMARY KEY REFERENCES agent_runs(run_token) ON DELETE CASCADE,
    alias TEXT NOT NULL,
    authorized NUMERIC(12, 4) NOT NULL CHECK (authorized > 0),
    models JSONB NOT NULL,
    ttl_seconds BIGINT NOT NULL CHECK (ttl_seconds > 0),
    state TEXT NOT NULL DEFAULT 'reserved'
        CHECK (state IN ('reserved', 'minting', 'issued', 'unknown', 'blocked', 'reconciled')),
    gateway_key_id TEXT NOT NULL DEFAULT '',
    observed_spend NUMERIC(12, 4),
    reconciled_spend NUMERIC(12, 4),
    reconciliation_evidence TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (observed_spend IS NULL OR observed_spend >= 0),
    CHECK (reconciled_spend IS NULL OR reconciled_spend >= 0)
);

CREATE VIEW run_budget_holds AS
SELECT r.run_token, r.shift_id,
       CASE
           WHEN a.run_token IS NULL THEN CASE WHEN r.state = 'running' THEN r.authorized ELSE 0 END
           WHEN a.state = 'reconciled' THEN GREATEST(COALESCE(a.observed_spend,0) - a.reconciled_spend,0)
           ELSE GREATEST(a.authorized, COALESCE(a.observed_spend, 0))
       END AS reserved
FROM agent_runs r LEFT JOIN run_llm_accounts a USING (run_token);

ALTER TABLE agent_runs ADD COLUMN outcome_digest TEXT NOT NULL DEFAULT '';
