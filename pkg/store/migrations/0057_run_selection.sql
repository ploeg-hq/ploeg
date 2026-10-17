ALTER TABLE work_items
    ADD COLUMN harness_requested TEXT CHECK (length(harness_requested) BETWEEN 1 AND 129),
    ADD COLUMN model_requested   TEXT CHECK (length(model_requested) BETWEEN 1 AND 256);

ALTER TABLE agent_runs
    ADD COLUMN harness_requested TEXT CHECK (length(harness_requested) BETWEEN 1 AND 129),
    ADD COLUMN model_selected    TEXT CHECK (length(model_selected) BETWEEN 1 AND 256);

CREATE INDEX agent_runs_claimable_harness ON agent_runs (team, role, harness_requested, id) WHERE state = 'pending';
