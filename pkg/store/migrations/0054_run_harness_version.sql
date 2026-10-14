-- ADR-0097: the Harness version a Run ran with and the agent's own name and
-- version, each written once. harness_recorded_at is when the version was
-- recorded, which the Teams read shows beside the newest observed version of
-- a Role. NULL is unknown: every Run recorded before this migration, and every
-- fact its worker did not report. Nothing is backfilled.
ALTER TABLE agent_runs
    ADD COLUMN harness_version     TEXT CHECK (length(harness_version) BETWEEN 1 AND 128),
    ADD COLUMN harness_agent       JSONB CHECK (jsonb_typeof(harness_agent) = 'object'),
    ADD COLUMN harness_recorded_at TIMESTAMPTZ;

CREATE INDEX agent_runs_observed_harness ON agent_runs (team, role, id DESC) WHERE harness_version IS NOT NULL;
