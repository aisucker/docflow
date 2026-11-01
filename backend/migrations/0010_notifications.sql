ALTER TABLE users ADD COLUMN notification_preferences jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK (jsonb_typeof(notification_preferences)='object');

CREATE TABLE notifications (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 task_id uuid REFERENCES workflow_tasks(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK (kind IN ('task_assigned','task_delegated','task_completed','document_returned','document_rejected','document_completed','due_soon','overdue')),
 title text NOT NULL,
 body text NOT NULL,
 deadline_at timestamptz,
 dedupe_key text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 read_at timestamptz,
 UNIQUE(user_id,dedupe_key)
);
CREATE INDEX notifications_user_id_idx ON notifications(user_id,id DESC);
CREATE INDEX notifications_unread_idx ON notifications(user_id,id DESC) WHERE read_at IS NULL;
