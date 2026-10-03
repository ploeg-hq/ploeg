-- ADR-0054: the card list finds the Work Items whose roster names a login.
-- Logins are compared lowercased, so each person column is indexed on
-- lower(). agent_runs gains a Work Item index for the card's minted time,
-- which the card list reads for every candidate.
CREATE INDEX pull_requests_by_merger ON pull_requests (lower(merged_by));
CREATE INDEX pull_request_reviews_by_reviewer ON pull_request_reviews (lower(reviewer));
CREATE INDEX gate_transitions_by_actor ON gate_transitions (lower(actor));
CREATE INDEX card_cracks_by_mender ON card_cracks (lower(mended_by));
CREATE INDEX agent_runs_by_work_item ON agent_runs (work_item_id, started_at);
