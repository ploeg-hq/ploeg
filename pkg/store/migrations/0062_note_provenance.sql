-- ADR-0122: every note says who wrote it, from what, whether its text is
-- trusted, at which level it is read, when it was observed and when it
-- expires. A note ploegd records itself (a review outcome) or a person writes
-- has no Run, and a promotion or a person's decision about a note waits in
-- note_reviews until it is applied.
ALTER TABLE work_item_note_outbox ALTER COLUMN run_id DROP NOT NULL;
ALTER TABLE work_item_note_outbox
    ADD COLUMN level         TEXT        NOT NULL DEFAULT 'work-item' CHECK (level IN ('work-item', 'repository')),
    ADD COLUMN client        TEXT        NOT NULL DEFAULT '',
    ADD COLUMN subject       TEXT        NOT NULL DEFAULT '',
    ADD COLUMN author_kind   TEXT        NOT NULL DEFAULT 'run' CHECK (author_kind IN ('run', 'person', 'ploeg')),
    ADD COLUMN author        TEXT        NOT NULL DEFAULT '',
    ADD COLUMN trust         TEXT        NOT NULL DEFAULT 'untrusted' CHECK (trust IN ('trusted', 'untrusted')),
    ADD COLUMN source_refs   TEXT[]      NOT NULL DEFAULT '{}',
    ADD COLUMN observed_at   TIMESTAMPTZ,
    ADD COLUMN expires_at    TIMESTAMPTZ,
    ADD COLUMN supersedes    TEXT        NOT NULL DEFAULT '',
    ADD COLUMN promoted_from TEXT        NOT NULL DEFAULT '',
    ADD COLUMN evidence      TEXT[]      NOT NULL DEFAULT '{}',
    ADD COLUMN approved_by   TEXT        NOT NULL DEFAULT '',
    ADD CONSTRAINT work_item_note_outbox_run_authored CHECK (author_kind <> 'run' OR (run_id IS NOT NULL AND trust = 'untrusted'));

UPDATE work_item_note_outbox SET author_kind = split_part(written_by, ':', 1), author = split_part(written_by, ':', 2)
WHERE split_part(written_by, ':', 1) IN ('run', 'ploeg');

CREATE INDEX work_item_note_outbox_by_work_item ON work_item_note_outbox (work_item_id);
CREATE INDEX work_item_note_outbox_by_client ON work_item_note_outbox (client) WHERE client <> '';

CREATE TABLE note_reviews (
    id             BIGSERIAL   PRIMARY KEY,
    kind           TEXT        NOT NULL CHECK (kind IN ('promotion', 'contradiction')),
    note_key       TEXT        NOT NULL,
    work_item_id   BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    repository     TEXT        NOT NULL DEFAULT '',
    client         TEXT        NOT NULL DEFAULT '',
    subject        TEXT        NOT NULL DEFAULT '',
    target_level   TEXT        NOT NULL DEFAULT '' CHECK (target_level IN ('', 'repository', 'client', 'tenant')),
    conflicts_with TEXT[]      NOT NULL DEFAULT '{}',
    evidence       TEXT[]      NOT NULL DEFAULT '{}',
    state          TEXT        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'rejected', 'resolved')),
    decision       TEXT        NOT NULL DEFAULT '',
    decided_by     TEXT        NOT NULL DEFAULT '',
    reason         TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at     TIMESTAMPTZ,
    applied_at     TIMESTAMPTZ,
    last_error     TEXT        NOT NULL DEFAULT '',
    UNIQUE (kind, note_key, target_level),
    CHECK ((kind = 'promotion') = (target_level <> ''))
);

CREATE INDEX note_reviews_open ON note_reviews (id) WHERE state = 'open';
CREATE INDEX note_reviews_to_apply ON note_reviews (id) WHERE state = 'accepted' AND applied_at IS NULL;
CREATE INDEX note_reviews_by_client ON note_reviews (client) WHERE client <> '';
