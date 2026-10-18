-- ADR-0119: a pull request is ready for review only when the forge's CI passed
-- on its head. One row per Shift and head commit the gate looked at: when it
-- started waiting for that commit's CI, when it asked the forge to re-run it
-- (once per commit), the Round it held the Shift at, so a held Shift does not
-- publish that Round's findings again on every sweep, and the failing checks
-- it handed to a fix round, so the writer's briefing names them. A new head
-- commit is a new row, so its wait and its one re-run start again.
CREATE TABLE forge_ci_gates (
    shift_id      BIGINT      NOT NULL REFERENCES shifts(id) ON DELETE CASCADE,
    head_sha      TEXT        NOT NULL,
    waiting_since TIMESTAMPTZ NOT NULL DEFAULT now(),
    rerun_at      TIMESTAMPTZ,
    held_round    INTEGER,
    fix_round     INTEGER,
    failing       JSONB,
    PRIMARY KEY (shift_id, head_sha)
);

CREATE INDEX forge_ci_gates_fix_round ON forge_ci_gates (shift_id, fix_round) WHERE fix_round IS NOT NULL;
