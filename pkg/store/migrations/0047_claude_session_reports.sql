-- ADR-0084: what the runner needs to open the pull request, and what its
-- post-session hook reports back once the session ended. The worker polls
-- the report; reported_at is set once and never cleared.
ALTER TABLE claude_sessions ADD COLUMN base_branch   TEXT        NOT NULL DEFAULT '';
ALTER TABLE claude_sessions ADD COLUMN title         TEXT        NOT NULL DEFAULT '';
ALTER TABLE claude_sessions ADD COLUMN reference     TEXT        NOT NULL DEFAULT '';
ALTER TABLE claude_sessions ADD COLUMN reported_at   TIMESTAMPTZ;
ALTER TABLE claude_sessions ADD COLUMN exit_reason   TEXT        NOT NULL DEFAULT '';
ALTER TABLE claude_sessions ADD COLUMN pushed_commit TEXT        NOT NULL DEFAULT '';
ALTER TABLE claude_sessions ADD COLUMN pull_request  TEXT        NOT NULL DEFAULT '';
ALTER TABLE claude_sessions ADD COLUMN final_message TEXT        NOT NULL DEFAULT '';
