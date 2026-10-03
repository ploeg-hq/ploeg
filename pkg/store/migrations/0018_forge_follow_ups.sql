ALTER TABLE work_items ADD COLUMN source_work_item_id BIGINT REFERENCES work_items (id) ON DELETE CASCADE;
ALTER TABLE work_items ADD COLUMN source_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN source_pr INT NOT NULL DEFAULT 0;

CREATE INDEX work_items_by_source
    ON work_items (source_work_item_id, source_branch)
    WHERE source_work_item_id IS NOT NULL;

CREATE INDEX shifts_by_branch ON shifts (branch);

CREATE TABLE work_item_reviews (
    id           BIGSERIAL   PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    provider     TEXT        NOT NULL,
    repo         TEXT        NOT NULL,
    pr           INT         NOT NULL DEFAULT 0,
    reviewer     TEXT        NOT NULL,
    body         TEXT        NOT NULL DEFAULT '',
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    shift_id     BIGINT      REFERENCES shifts (id) ON DELETE CASCADE,
    round        INT         NOT NULL DEFAULT 0
);

CREATE INDEX work_item_reviews_pending
    ON work_item_reviews (work_item_id) WHERE shift_id IS NULL;

CREATE INDEX work_item_reviews_by_shift ON work_item_reviews (shift_id, round);
