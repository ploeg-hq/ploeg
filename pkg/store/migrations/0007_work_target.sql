-- The repository a Work Item's changes land in belongs to the WORK, not to the
-- Team (design §3: a Team is a capability manifest — name, roles, strategy,
-- budget — and names no repository). Resolved at ingest from external_scope
-- (the tracker's own container id; Vikunja project_id) and pinned on the row so
-- the audit answers "why did run X push to repo Y" from rows, not from whatever
-- a config file happened to say at the time.
--
-- Empty target_owner/target_repo = unresolved: the worker falls back to its
-- env-configured repo, which is exactly the pre-decoupling behavior. That
-- fallback is what makes every intermediate state of the migration a no-op.
--
-- ADD COLUMN ... NOT NULL DEFAULT '' is metadata-only on PG11+ (fast default,
-- no table rewrite) — same shape as 0002/0004/0005/0006.
--
-- DELIBERATELY NO INDEX CHANGE. work_items_claimable
-- (team, priority DESC, created_at) WHERE state='queued' serves store.Claim,
-- store.QueueDepth AND the KEDA scaler's trigger query; nothing here touches
-- its key columns or its predicate. Keep it that way.
ALTER TABLE work_items ADD COLUMN external_scope     TEXT NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN target_forge       TEXT NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN target_owner       TEXT NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN target_repo        TEXT NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN target_base_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN route_rule         TEXT NOT NULL DEFAULT '';
