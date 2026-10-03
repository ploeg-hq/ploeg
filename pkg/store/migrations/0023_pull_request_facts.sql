-- ADR-0045: keep what the forge says about each Ploeg pull request. A NULL
-- column is a fact the forge has not reported yet, never a default.
CREATE TABLE pull_requests (
    id               BIGSERIAL   PRIMARY KEY,
    forge            TEXT        NOT NULL,
    repo_owner       TEXT        NOT NULL,
    repo_name        TEXT        NOT NULL,
    number           INT         NOT NULL CHECK (number > 0),
    work_item_id     BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    shift_id         BIGINT      REFERENCES shifts (id) ON DELETE SET NULL,
    branch           TEXT,
    state            TEXT        CHECK (state IN ('open', 'merged', 'closed')),
    head_sha         TEXT,
    merge_commit_sha TEXT,
    merged_at        TIMESTAMPTZ,
    merged_by        TEXT,
    closed_at        TIMESTAMPTZ,
    first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (forge, repo_owner, repo_name, number)
);

CREATE INDEX pull_requests_by_work_item ON pull_requests (work_item_id, updated_at DESC);

-- Every review the forge reported on a Ploeg pull request, whatever its
-- verdict. work_item_reviews stays the queue of requests for changes a
-- writing Round receives.
CREATE TABLE pull_request_reviews (
    id              BIGSERIAL   PRIMARY KEY,
    pull_request_id BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    reviewer        TEXT        NOT NULL DEFAULT '',
    state           TEXT        CHECK (state IN ('approved', 'changes_requested', 'commented')),
    head_sha        TEXT,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX pull_request_reviews_by_pull_request ON pull_request_reviews (pull_request_id, id);

-- Every existing row was recorded by RecordChangesRequested, so the default
-- is the truth for them.
ALTER TABLE work_item_reviews ADD COLUMN state TEXT NOT NULL DEFAULT 'changes_requested'
    CHECK (state IN ('approved', 'changes_requested', 'commented'));
ALTER TABLE work_item_reviews ADD COLUMN head_sha TEXT;
