-- ADR-0115: a chat message is a read-only Run with the Role 'chat' about no
-- Work Item, outside any Shift, Round or Lease, paid from the Team's monthly
-- Chat Allowance (purpose 'chat' in inference_allowances).
--
-- Every other Run belongs to a Work Item. Only a chat Run may leave it empty,
-- and such a Run never writes and has no Shift.
ALTER TABLE agent_runs ALTER COLUMN work_item_id DROP NOT NULL;

ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_work_item_or_chat
    CHECK (work_item_id IS NOT NULL OR (role = 'chat' AND shift_id IS NULL AND NOT writes));

-- The chat message's own facts beside its agent_runs row. The message is
-- kept only as its SHA-256; the consumer keeps the text. (consumer, chat_id)
-- is the consumer's idempotency key.
CREATE TABLE chats (
    run_id         BIGINT      PRIMARY KEY REFERENCES agent_runs (id) ON DELETE CASCADE,
    consumer       TEXT        NOT NULL,
    chat_id        TEXT        NOT NULL,
    actor          TEXT        NOT NULL,
    asked_by       TEXT        NOT NULL DEFAULT '',
    team           TEXT        NOT NULL,
    allowance_id   BIGINT      NOT NULL REFERENCES inference_allowances (id),
    message_sha256 TEXT        NOT NULL CHECK (message_sha256 ~ '^[0-9a-f]{64}$'),
    fingerprint    TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ,
    UNIQUE (consumer, chat_id)
);

CREATE INDEX chats_by_allowance ON chats (allowance_id);
