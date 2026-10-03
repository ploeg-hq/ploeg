-- The reviewer's verdict (ADR-0017): the one bit that may re-open the plan's
-- own writing Round.
--
-- Deliberately an enum-by-CHECK rather than free text. What makes the review
-- loop safe to run unattended is that a verdict cannot say anything except
-- "approve" or "request_changes" — it names no Role, authors no Round, raises
-- no budget. A TEXT column with no constraint would let a typo, or a creative
-- agent, mean something the engine never agreed to.
--
-- Empty is the normal case: writers never carry one, and a reading Run that
-- offers none simply lets the plan end as it does today.
ALTER TABLE agent_runs ADD COLUMN verdict TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_verdict
    CHECK (verdict IN ('', 'approve', 'request_changes'));
