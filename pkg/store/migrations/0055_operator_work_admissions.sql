-- ADR-0102: an Operator Consumer admits a manual-origin Work Item that
-- ploeg-worker executes. One row per Admission, written in the transaction
-- that creates the Work Item. (consumer, request_id) is the consumer's
-- idempotency key: a replay with the same actor and fingerprint returns
-- work_item_id and changes nothing, any other replay is a conflict. The
-- fingerprint is a SHA-256 of the request without its request_id; the
-- request's title and description live on the Work Item only.
CREATE TABLE operator_work_admissions (
    consumer     TEXT        NOT NULL CHECK (length(consumer) BETWEEN 1 AND 128),
    request_id   TEXT        NOT NULL CHECK (length(request_id) BETWEEN 1 AND 128),
    actor        TEXT        NOT NULL CHECK (length(actor) BETWEEN 1 AND 128),
    fingerprint  TEXT        NOT NULL CHECK (length(fingerprint) = 64),
    work_item_id BIGINT      NOT NULL UNIQUE REFERENCES work_items (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, request_id)
);
