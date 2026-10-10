-- ADR-0080: the Run card left Ploeg. An operator consumer imported the rows a
-- person or a freeze created through the card legacy export of 0.2.0-rc.10
-- and rc.11 before this release, so the card tables and the derived pull
-- request figures go. The delivery facts they were computed from stay.
DROP TABLE IF EXISTS card_cracks;
DROP TABLE IF EXISTS card_rarity;
DROP TABLE IF EXISTS card_comments;

ALTER TABLE pull_requests
    DROP COLUMN IF EXISTS kpis,
    DROP COLUMN IF EXISTS kpis_computed_at,
    DROP COLUMN IF EXISTS shape;
