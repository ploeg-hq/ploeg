ALTER TABLE operator_executions ADD COLUMN repository_id TEXT NOT NULL DEFAULT '';
ALTER TABLE operator_executions ADD COLUMN repository_url TEXT NOT NULL DEFAULT '';
ALTER TABLE operator_executions ADD COLUMN base_branch TEXT NOT NULL DEFAULT '';

CREATE TABLE operator_delivery_candidates (
    id TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL UNIQUE REFERENCES operator_executions(id) ON DELETE CASCADE,
    work_item_id BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE operator_delivery_receipts (
    id TEXT PRIMARY KEY,
    candidate_id TEXT NOT NULL UNIQUE REFERENCES operator_delivery_candidates(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE operator_delivery_approvals (
    id TEXT PRIMARY KEY,
    candidate_id TEXT NOT NULL UNIQUE REFERENCES operator_delivery_candidates(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE operator_publication_operations (
    id TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL UNIQUE REFERENCES operator_executions(id) ON DELETE CASCADE,
    candidate_id TEXT NOT NULL UNIQUE REFERENCES operator_delivery_candidates(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','unknown','published')),
    data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
