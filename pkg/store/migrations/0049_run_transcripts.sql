-- ADR-0090: a Run's transcript, the ordered entries the worker derives from
-- the agent's session updates. The worker numbers them; a resent batch is
-- ignored row by row. bytes is each entry's JSON size, summed against the
-- per-Run limit. Entries live and die with their Run.
CREATE TABLE run_transcript_entries (
    run_id      BIGINT      NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    seq         INTEGER     NOT NULL CHECK (seq > 0),
    at          TIMESTAMPTZ NOT NULL,
    kind        TEXT        NOT NULL CHECK (kind IN ('message', 'thought', 'tool_call', 'tool_output', 'plan', 'stage', 'truncated')),
    body        JSONB       NOT NULL,
    bytes       INTEGER     NOT NULL CHECK (bytes > 0),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);
