-- ADR-0112: a claude-environment Run that the person's own Claude Code
-- carries. A worker never claims a Run whose carrier is client; it waits for
-- the person it acts for, who claims it through the operator API.
ALTER TABLE agent_runs ADD COLUMN carrier TEXT CHECK (carrier = 'client');

-- The person a Work Item's client-carried Runs act for: Ploeg's person id of
-- its admission's acting person, or of the tracker user who handed it, when
-- configuration maps that user to one. handed_by is that tracker user, kept
-- so a Work Item can name them while no person id is known. Empty is unknown.
ALTER TABLE work_items
    ADD COLUMN acts_for  TEXT NOT NULL DEFAULT '',
    ADD COLUMN handed_by TEXT NOT NULL DEFAULT '';
