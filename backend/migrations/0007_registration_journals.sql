CREATE TABLE registration_journals (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('project','registration')),
 number_pattern text NOT NULL DEFAULT '{seq}/{year}',
 number_width integer NOT NULL DEFAULT 4 CHECK(number_width BETWEEN 1 AND 12),
 next_number bigint NOT NULL DEFAULT 1 CHECK(next_number BETWEEN 1 AND 999999999999),
 active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE document_types ADD COLUMN project_journal_id uuid REFERENCES registration_journals(id);
ALTER TABLE document_types ADD COLUMN registration_journal_id uuid REFERENCES registration_journals(id);
ALTER TABLE documents ADD COLUMN project_number text;
ALTER TABLE documents ADD COLUMN project_numbered_at timestamptz;
ALTER TABLE documents ADD COLUMN project_journal_id uuid REFERENCES registration_journals(id);
ALTER TABLE documents ADD COLUMN registration_journal_id uuid REFERENCES registration_journals(id);
CREATE TABLE journal_entries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 journal_id uuid NOT NULL REFERENCES registration_journals(id),
 document_id uuid NOT NULL REFERENCES documents(id),
 kind text NOT NULL CHECK(kind IN ('project','registration')),
 number text NOT NULL,
 sequence_number bigint NOT NULL,
 actor_id uuid REFERENCES users(id),
 assigned_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(journal_id,number),
 UNIQUE(document_id,kind)
);
CREATE INDEX journal_entries_document_idx ON journal_entries(document_id);
DROP INDEX doc_registration_number_unique;
CREATE UNIQUE INDEX doc_registration_number_unique ON documents(registration_number) WHERE registration_number IS NOT NULL AND registration_journal_id IS NULL AND deleted_at IS NULL;
UPDATE workflow_buttons SET label='Зарегистрировать' WHERE code='register';
