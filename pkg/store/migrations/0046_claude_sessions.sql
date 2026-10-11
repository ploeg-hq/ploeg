-- ADR-0084: a Run executed as a Claude Code session in a Team's self-hosted
-- environment. The worker records the session it dispatched; a runner's
-- session hook later proves it is that session with Anthropic's session token
-- and exchanges the proof for the Run's push credential.
--
-- session_id is stored without its cse_ or session_ prefix. One session
-- serves one Run, and one Run dispatches at most one session.
CREATE TABLE claude_sessions (
    run_id         BIGINT      PRIMARY KEY REFERENCES agent_runs (id) ON DELETE CASCADE,
    environment_id TEXT        NOT NULL CHECK (environment_id LIKE 'ccpool\_%'),
    session_id     TEXT        NOT NULL CHECK (session_id <> ''),
    recorded_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, session_id)
);
