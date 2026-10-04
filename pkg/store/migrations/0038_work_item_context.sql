-- Context bundles (proposed, proof of concept): files a person attaches to a
-- Work Item. Every Run claimed after added_at is given them. The bytes live
-- here for the proof of concept; object storage is a spike. shift_id is the
-- Shift that was open at upload, and phase says whether a Run of it had
-- already started ('while_steering') or not ('before_start').
CREATE TABLE work_item_context (
    id           TEXT        PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    sha256       TEXT        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    name         TEXT        NOT NULL,
    media_type   TEXT        NOT NULL,
    bytes        BIGINT      NOT NULL CHECK (bytes > 0),
    files        INT         NOT NULL CHECK (files > 0),
    content      BYTEA       NOT NULL,
    note         TEXT        NOT NULL DEFAULT '',
    added_by     TEXT        NOT NULL,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    shift_id     BIGINT      REFERENCES shifts (id) ON DELETE SET NULL,
    phase        TEXT        NOT NULL CHECK (phase IN ('before_start', 'while_steering')),
    UNIQUE (work_item_id, sha256)
);

CREATE INDEX work_item_context_by_item ON work_item_context (work_item_id, added_at);
