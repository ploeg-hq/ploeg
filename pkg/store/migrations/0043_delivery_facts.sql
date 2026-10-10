-- ADR-0079: Ploeg supplies delivery facts and a consumer computes its own
-- figures from them. Each changed file of a merged pull request keeps the
-- raw indentation measurements Ploeg takes from the diff it never stores.
-- All five are NULL while the diff was not measured.
ALTER TABLE pull_request_files
    ADD COLUMN indent_method    TEXT CHECK (length(indent_method) BETWEEN 1 AND 64),
    ADD COLUMN indent_unit      INT  CHECK (indent_unit >= 1),
    ADD COLUMN indent_added     INT  CHECK (indent_added >= 0),
    ADD COLUMN indent_removed   INT  CHECK (indent_removed >= 0),
    ADD COLUMN indent_max_depth INT  CHECK (indent_max_depth >= 0);

-- One comment a consumer keeps on a pull request under its own key, posted
-- with Ploeg's forge credentials. The comment is found on the forge by its
-- marker; comment_id is what the forge returned for it last. actor is the
-- audited operator actor of the last write.
CREATE TABLE pull_request_comments (
    pull_request_id BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    key             TEXT        NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    comment_id      BIGINT,
    image_url       TEXT,
    actor           TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pull_request_id, key)
);
