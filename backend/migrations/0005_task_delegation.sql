ALTER TABLE workflow_tasks ADD COLUMN delegated_from uuid REFERENCES workflow_tasks(id);
CREATE UNIQUE INDEX workflow_tasks_one_delegate ON workflow_tasks(delegated_from) WHERE delegated_from IS NOT NULL;
