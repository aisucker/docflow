CREATE TABLE IF NOT EXISTS departments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, parent_id uuid REFERENCES departments(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS positions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text NOT NULL UNIQUE, password_hash text NOT NULL,
 full_name text NOT NULL, role text NOT NULL DEFAULT 'employee' CHECK(role IN ('admin','registrar','employee')),
 department_id uuid REFERENCES departments(id), position_id uuid REFERENCES positions(id), active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS refresh_sessions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 token_hash bytea NOT NULL UNIQUE, expires_at timestamptz NOT NULL, revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS document_types (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), code text NOT NULL UNIQUE, name text NOT NULL,
 fields jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(fields)='array'), active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS documents (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), type_id uuid NOT NULL REFERENCES document_types(id),
 registration_number text, title text NOT NULL, summary text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','in_review','approved','rejected','returned')),
 author_id uuid NOT NULL REFERENCES users(id), assignee_id uuid REFERENCES users(id),
 department_id uuid REFERENCES departments(id), due_at timestamptz, registered_at timestamptz,
 fields jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(fields)='object'),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz, deleted_by uuid REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS documents_journal_idx ON documents(status,created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS documents_type_idx ON documents(type_id,created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS documents_fields_gin ON documents USING gin(fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS documents_search_idx ON documents USING gin(to_tsvector('simple',coalesce(title,'')||' '||coalesce(summary,'')||' '||coalesce(registration_number,'')));
CREATE UNIQUE INDEX IF NOT EXISTS doc_registration_number_unique ON documents(registration_number) WHERE registration_number IS NOT NULL AND deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS workflow_definitions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workflow_key text NOT NULL, version integer NOT NULL CHECK(version > 0),
 name text NOT NULL, doc_type_id uuid REFERENCES document_types(id), definition jsonb NOT NULL,
 active boolean NOT NULL DEFAULT true, created_by uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workflow_key,version)
);
CREATE TABLE IF NOT EXISTS workflow_instances (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), document_id uuid NOT NULL REFERENCES documents(id), definition_id uuid NOT NULL REFERENCES workflow_definitions(id),
 definition_snapshot jsonb NOT NULL, current_step integer NOT NULL DEFAULT 0,
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','approved','rejected','returned')),
 started_by uuid NOT NULL REFERENCES users(id), started_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_workflow_per_doc ON workflow_instances(document_id) WHERE status='active';
CREATE TABLE IF NOT EXISTS workflow_tasks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), instance_id uuid NOT NULL REFERENCES workflow_instances(id), document_id uuid NOT NULL REFERENCES documents(id),
 step_index integer NOT NULL, step_id text NOT NULL, title text NOT NULL, assignee_id uuid NOT NULL REFERENCES users(id),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected','returned','cancelled')),
 comment text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), decided_at timestamptz
);
CREATE INDEX IF NOT EXISTS workflow_tasks_assignee_idx ON workflow_tasks(assignee_id,status,created_at DESC);
CREATE TABLE IF NOT EXISTS document_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, document_id uuid NOT NULL REFERENCES documents(id), actor_id uuid REFERENCES users(id),
 kind text NOT NULL, detail jsonb NOT NULL DEFAULT '{}'::jsonb, occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS document_events_doc_idx ON document_events(document_id,occurred_at);
CREATE TABLE IF NOT EXISTS files (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), document_id uuid NOT NULL REFERENCES documents(id),
 filename text NOT NULL, mime_type text NOT NULL, storage_key text NOT NULL, size bigint NOT NULL CHECK(size >= 0),
 version integer NOT NULL, uploaded_by uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(document_id,filename,version)
);
CREATE TABLE IF NOT EXISTS templates (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, type_id uuid REFERENCES document_types(id),
 filename text NOT NULL, storage_key text NOT NULL, created_by uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS integration_tokens (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, token_hash bytea NOT NULL UNIQUE, active boolean NOT NULL DEFAULT true,
 created_by uuid REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS webhooks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, url text NOT NULL, secret text NOT NULL, active boolean NOT NULL DEFAULT true,
 created_by uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS outbox (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, event_type text NOT NULL, payload jsonb NOT NULL,
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(), delivered_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
