ALTER TABLE operator_executions ADD COLUMN source JSONB;
ALTER TABLE work_items ADD COLUMN operator_owned BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE work_items SET operator_owned=TRUE WHERE id IN (SELECT work_item_id FROM operator_executions);
