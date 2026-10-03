CREATE INDEX audit_log_by_item ON audit_log (work_item_id, id) WHERE work_item_id IS NOT NULL;

CREATE INDEX audit_log_reconciled ON audit_log (at) WHERE action = 'llm.reconciled';

CREATE INDEX agent_runs_finished ON agent_runs (finished_at) WHERE finished_at IS NOT NULL;

CREATE INDEX run_llm_accounts_holding ON run_llm_accounts (run_token)
    WHERE state <> 'reconciled' OR COALESCE(observed_spend, 0) > reconciled_spend;
