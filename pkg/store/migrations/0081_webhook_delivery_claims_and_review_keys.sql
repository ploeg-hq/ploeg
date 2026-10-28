ALTER TABLE forge_deliveries ADD COLUMN claim TEXT;
ALTER TABLE forge_deliveries ADD COLUMN claimed_until TIMESTAMPTZ;
ALTER TABLE forge_deliveries ADD COLUMN completed_at TIMESTAMPTZ;
UPDATE forge_deliveries SET completed_at = seen_at;

ALTER TABLE work_item_reviews ADD COLUMN review_key TEXT CHECK (length(review_key) BETWEEN 1 AND 160);
CREATE UNIQUE INDEX work_item_reviews_once_per_forge_review
    ON work_item_reviews (work_item_id, provider, repo, pr, review_key)
    WHERE review_key IS NOT NULL;
