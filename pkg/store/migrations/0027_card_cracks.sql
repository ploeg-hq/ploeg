-- ADR-0052: what a merged pull request changed, kept at merge so a later
-- bug fix can be matched against it. files_captured_at is NULL while the
-- forge has not been read; files_truncated is true when the pull request
-- touched more files than Ploeg keeps. labels are the label names the forge
-- reported at merge, NULL when it was not read.
ALTER TABLE pull_requests ADD COLUMN files_captured_at TIMESTAMPTZ;
ALTER TABLE pull_requests ADD COLUMN files_truncated   BOOLEAN;
ALTER TABLE pull_requests ADD COLUMN labels            TEXT[];

CREATE TABLE pull_request_files (
    pull_request_id BIGINT NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    path            TEXT   NOT NULL CHECK (length(path) BETWEEN 1 AND 1024),
    PRIMARY KEY (pull_request_id, path)
);

CREATE INDEX pull_request_files_by_path ON pull_request_files (path);

-- A merged pull request that reverted a Ploeg play. The reverting pull
-- request need not be a Ploeg play. matched_by is title when its title began
-- with "Revert" and named the play's number, and commit when one of its
-- commits said "This reverts commit <sha>" of the play's merge or head commit.
CREATE TABLE pull_request_reverts (
    pull_request_id  BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    forge            TEXT        NOT NULL,
    repo_owner       TEXT        NOT NULL,
    repo_name        TEXT        NOT NULL,
    number           INT         NOT NULL CHECK (number > 0),
    merge_commit_sha TEXT,
    merged_at        TIMESTAMPTZ,
    merged_by        TEXT,
    matched_by       TEXT        NOT NULL CHECK (matched_by IN ('title', 'commit')),
    detected_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pull_request_id, forge, repo_owner, repo_name, number)
);

-- One attribution of a bug Work Item to the card (Work Item) whose play
-- caused it. Only a human proposes, confirms, disputes or resolves one; Ploeg
-- itself only proposes candidates and records mends. steward is the card's
-- steward when the crack was proposed. A row is never deleted: an unlinked or
-- evolved attribution stays as history.
CREATE TABLE card_cracks (
    id                   BIGSERIAL   PRIMARY KEY,
    team                 TEXT        NOT NULL,
    card_work_item_id    BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    pull_request_id      BIGINT      REFERENCES pull_requests (id) ON DELETE SET NULL,
    bug_work_item_id     BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    state                TEXT        NOT NULL CHECK (state IN ('proposed', 'confirmed', 'disputed', 'unlinked', 'evolved')),
    severity             TEXT        CHECK (severity IN ('S1', 'S2', 'S3', 'S4')),
    share                TEXT        CHECK (share IN ('primary', 'contributing')),
    discovery            TEXT        CHECK (discovery IN ('self', 'discovered', 'concealed')),
    steward              TEXT        NOT NULL DEFAULT '',
    note                 TEXT,
    proposed_by          TEXT        NOT NULL,
    proposed_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_by         TEXT,
    confirmed_at         TIMESTAMPTZ,
    dispute_until        TIMESTAMPTZ,
    disputed_by          TEXT,
    disputed_at          TIMESTAMPTZ,
    dispute_reason       TEXT,
    resolved_by          TEXT,
    resolved_at          TIMESTAMPTZ,
    resolution           TEXT        CHECK (resolution IN ('upheld', 'unlinked')),
    evolved_by           TEXT,
    evolved_at           TIMESTAMPTZ,
    mend_pull_request_id BIGINT      REFERENCES pull_requests (id) ON DELETE SET NULL,
    mend_number          INT,
    mended_at            TIMESTAMPTZ,
    mended_by            TEXT,
    mend_by_steward      BOOLEAN,
    mend_confirmed_at    TIMESTAMPTZ,
    mend_reopened_at     TIMESTAMPTZ,
    UNIQUE (card_work_item_id, bug_work_item_id),
    CHECK (card_work_item_id <> bug_work_item_id),
    CHECK (state = 'evolved' OR (severity IS NOT NULL AND share IS NOT NULL AND discovery IS NOT NULL))
);

CREATE INDEX card_cracks_by_bug ON card_cracks (bug_work_item_id);
CREATE INDEX card_cracks_open_mends ON card_cracks (mended_at)
    WHERE mended_at IS NOT NULL AND mend_confirmed_at IS NULL AND mend_reopened_at IS NULL;
