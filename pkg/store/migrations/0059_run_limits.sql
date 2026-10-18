-- ADR-0114: the limits that stop a Run's prompt, as its worker reported them
-- when the prompt started, written once. A NULL limit beside a recorded
-- prompt_started_at is no limit; a NULL prompt_started_at is unknown, which is
-- every Run recorded before this migration. Nothing is backfilled.
ALTER TABLE agent_runs
    ADD COLUMN prompt_started_at    TIMESTAMPTZ,
    ADD COLUMN limit_prompt_seconds INTEGER CHECK (limit_prompt_seconds BETWEEN 1 AND 604800),
    ADD COLUMN limit_idle_seconds   INTEGER CHECK (limit_idle_seconds BETWEEN 1 AND 604800),
    ADD CONSTRAINT agent_runs_limits_need_prompt_start
        CHECK (prompt_started_at IS NOT NULL OR (limit_prompt_seconds IS NULL AND limit_idle_seconds IS NULL));
