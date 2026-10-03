ALTER TABLE work_items ADD COLUMN next_eligible_at TIMESTAMPTZ;
ALTER TABLE work_items ADD COLUMN infra_failures INT NOT NULL DEFAULT 0;
