-- The review reconcile claims each awaiting_review Work Item before it asks
-- the forge about its pull request, and the claim holds until
-- review_claimed_until, so with several ploegd replicas one pull request is
-- read once per interval instead of once per replica (as deploy checks do,
-- ADR-0047). NULL is never claimed.
ALTER TABLE work_items ADD COLUMN review_claimed_until TIMESTAMPTZ;
