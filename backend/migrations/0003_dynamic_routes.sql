CREATE TABLE workflow_groups (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL UNIQUE,
 active boolean NOT NULL DEFAULT true
);
CREATE TABLE workflow_group_members (
 group_id uuid NOT NULL REFERENCES workflow_groups(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 PRIMARY KEY(group_id,user_id)
);
CREATE INDEX workflow_group_members_user_idx ON workflow_group_members(user_id);
ALTER TABLE document_types ADD COLUMN default_workflow_key text;
ALTER TABLE documents ADD COLUMN workflow_key text;
