-- ADR-0156: how Ploeg knows the person a Work Item's Runs act for, and the
-- party the identity provider named as acting for them. acts_for_proof is
-- 'proven' when a verified person token named the person at admission
-- (ADR-0150) and 'asserted' when an Operator Consumer's header or the
-- tracker's people mapping did; empty with an empty acts_for. Rows written
-- before this migration keep it empty and read as asserted. acts_for_act is
-- the person token's act.sub, empty when it had none.
ALTER TABLE work_items
    ADD COLUMN acts_for_proof TEXT NOT NULL DEFAULT '' CHECK (acts_for_proof IN ('', 'proven', 'asserted')),
    ADD COLUMN acts_for_act   TEXT NOT NULL DEFAULT '';
