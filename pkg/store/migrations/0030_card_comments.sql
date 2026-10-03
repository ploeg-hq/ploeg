-- ADR-0055: the one card comment Ploeg keeps on a Work Item's pull request.
-- moment is the key of the last moment the comment showed, empty until the
-- first one was posted; pull_request_id is the play it was posted on.
-- checked_at is when the sweep last assembled the card to look for a new
-- moment, so a sweep reads the longest-unchecked cards first. The comment
-- itself is found on the forge by its marker; comment_id is what the forge
-- returned for it last, kept for the log.
CREATE TABLE card_comments (
    work_item_id    BIGINT      PRIMARY KEY REFERENCES work_items (id) ON DELETE CASCADE,
    pull_request_id BIGINT      REFERENCES pull_requests (id) ON DELETE SET NULL,
    moment          TEXT        NOT NULL DEFAULT '' CHECK (length(moment) <= 256),
    comment_id      BIGINT,
    image           BOOLEAN     NOT NULL DEFAULT false,
    published_at    TIMESTAMPTZ,
    checked_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX card_comments_by_check ON card_comments (checked_at);
CREATE INDEX pull_requests_merged_at ON pull_requests (merged_at) WHERE state = 'merged';
