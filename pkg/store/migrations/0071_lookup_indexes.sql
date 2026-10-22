-- Indexes for lookups that scanned a whole table per row. Each one is proven
-- to be chosen by the planner in pkg/store/lookup_indexes_test.go.

-- The latest Shift of a Work Item (ORDER BY id DESC LIMIT 1) walked
-- shifts_pkey backwards; the only work_item_id index covers live Shifts.
CREATE INDEX shifts_by_work_item ON shifts (work_item_id, id);

-- UnsettledLLMAccounts asks whether a mint started for an alias with
-- IS NOT DISTINCT FROM on work_item_id, which audit_log_by_item cannot serve.
CREATE INDEX audit_log_llm_mint_alias ON audit_log ((detail->>'alias'))
    WHERE action IN ('llm.minting', 'llm.issued', 'llm.unknown');

-- The open-Shift pull request metric probes pull_requests by shift_id.
CREATE INDEX pull_requests_by_shift ON pull_requests (shift_id) WHERE shift_id IS NOT NULL;

-- Claims and the sweep exclude operator-executed Runs and Shifts with
-- NOT EXISTS on these foreign keys.
CREATE INDEX operator_executions_by_run ON operator_executions (run_id);
CREATE INDEX operator_executions_by_shift ON operator_executions (shift_id);

-- A Work Item's human change requests, read per row by the operator API.
CREATE INDEX work_item_reviews_by_work_item ON work_item_reviews (work_item_id, state);
