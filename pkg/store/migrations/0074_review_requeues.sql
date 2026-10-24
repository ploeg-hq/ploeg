ALTER TABLE work_items
    ADD COLUMN review_requeues INTEGER NOT NULL DEFAULT 0 CHECK (review_requeues >= 0);
