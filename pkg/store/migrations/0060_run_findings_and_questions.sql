-- ADR-0121: findings by id, a writer's answers to them, the questions a Run
-- asks and the answers Runs give, each as the Run reported them after ploegd
-- assigned the ids. NULL is a Run that reported none, which is every Run
-- recorded before this migration.
ALTER TABLE agent_runs
    ADD COLUMN finding_items    JSONB CHECK (finding_items IS NULL OR jsonb_typeof(finding_items) = 'array'),
    ADD COLUMN finding_answers  JSONB CHECK (finding_answers IS NULL OR jsonb_typeof(finding_answers) = 'array'),
    ADD COLUMN questions        JSONB CHECK (questions IS NULL OR jsonb_typeof(questions) = 'array'),
    ADD COLUMN question_answers JSONB CHECK (question_answers IS NULL OR jsonb_typeof(question_answers) = 'array');

-- A person's answer to a question a Run addressed to a person. One answer per
-- question; the next Runs of the Work Item are briefed with it.
CREATE TABLE question_answers (
    id           BIGSERIAL   PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    question_id  TEXT        NOT NULL CHECK (question_id ~ '^q[1-9][0-9]{0,18}-[1-9][0-9]{0,3}$'),
    consumer     TEXT        NOT NULL,
    command_id   TEXT        NOT NULL,
    actor        TEXT        NOT NULL,
    answer       TEXT        NOT NULL CHECK (length(answer) BETWEEN 1 AND 4096),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (work_item_id, question_id),
    UNIQUE (consumer, command_id)
);

-- A requeue that resumes a Shift parked on a question to a person: the Round
-- of the parked Shift that asked. NULL is a full restart, which is every
-- requeue recorded before this migration.
ALTER TABLE work_item_requeues
    ADD COLUMN resumes_round INTEGER CHECK (resumes_round IS NULL OR resumes_round >= 1);
