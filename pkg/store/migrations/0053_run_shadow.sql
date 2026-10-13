-- ADR-0094: a shadow Run reads beside the Round's reviewer and its verdict and
-- findings are recorded without acting. The flag is copied from the Team's
-- plan when the Round opens, so a later plan change does not relabel a Run.
-- Every Run recorded before this migration was not a shadow.
ALTER TABLE agent_runs
    ADD COLUMN shadow BOOLEAN NOT NULL DEFAULT false,
    ADD CONSTRAINT agent_runs_shadow_reads CHECK (NOT (shadow AND writes));
