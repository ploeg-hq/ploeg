-- ADR-0124: the version vector a Run's briefing was read at, what the briefing
-- held, and each catch-up the Run was given at a checkpoint. ploegd writes the
-- Ploeg sequence and tracker revision at the claim; the worker reports the
-- forge commit, graph commits, briefed note keys, concept paths and section
-- budgets once, in its briefed checkpoint. Every column is NULL for a Run
-- recorded before this migration, which reads as unknown. Nothing is
-- backfilled.
ALTER TABLE agent_runs
    ADD COLUMN board_claimed_at       TIMESTAMPTZ,
    ADD COLUMN board_ploeg_sequence   BIGINT CHECK (board_ploeg_sequence >= 0),
    ADD COLUMN board_tracker_revision TEXT,
    ADD COLUMN board_briefed_at       TIMESTAMPTZ,
    ADD COLUMN board_forge_commit     TEXT CHECK (board_forge_commit ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    ADD COLUMN board_notes_commit     TEXT CHECK (length(board_notes_commit) <= 128),
    ADD COLUMN board_concepts_commit  TEXT CHECK (length(board_concepts_commit) <= 128),
    ADD COLUMN briefed_notes          TEXT[] CHECK (cardinality(briefed_notes) <= 200),
    ADD COLUMN briefed_concepts       TEXT[] CHECK (cardinality(briefed_concepts) <= 200),
    ADD COLUMN briefing_sections      JSONB,
    ADD CONSTRAINT agent_runs_briefing_needs_claim
        CHECK (board_briefed_at IS NULL OR board_claimed_at IS NOT NULL);

-- Finds every Run briefed with one note, for the usefulness query in
-- docs/ops/briefings.md.
CREATE INDEX agent_runs_briefed_notes ON agent_runs USING gin (briefed_notes) WHERE briefed_notes IS NOT NULL;

CREATE TABLE run_board_catch_ups (
    id          BIGSERIAL   PRIMARY KEY,
    run_id      BIGINT      NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    checkpoint  TEXT        NOT NULL CHECK (checkpoint IN ('refresh', 'before_finish')),
    delivery    TEXT        NOT NULL CHECK (delivery IN ('file', 'prompt', 'next_round')),
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    versions    JSONB       NOT NULL,
    changes     JSONB       NOT NULL,
    digest      TEXT        NOT NULL
);

CREATE INDEX run_board_catch_ups_by_run ON run_board_catch_ups (run_id, id);
