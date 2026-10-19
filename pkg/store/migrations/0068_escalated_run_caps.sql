-- ADR-0130: the cap a writing Run escalated under onCapReached: escalate is
-- authorized at. The claim uses it in place of the Role's cap, still bounded
-- by what the Shift's pool has left. NULL means the Role's cap.
ALTER TABLE agent_runs ADD COLUMN escalated_cap NUMERIC(12, 4) CHECK (escalated_cap > 0);
