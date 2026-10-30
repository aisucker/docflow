ALTER TABLE users ADD COLUMN signing_iin text NOT NULL DEFAULT '' CHECK(signing_iin='' OR signing_iin ~ '^[0-9]{12}$');
INSERT INTO workflow_buttons(code,label,effect,style,comment_required) VALUES('sign','Подписать ЭЦП','complete','primary',false);
CREATE TABLE signing_challenges (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 document_id uuid NOT NULL REFERENCES documents(id),
 task_id uuid NOT NULL REFERENCES workflow_tasks(id),
 user_id uuid NOT NULL REFERENCES users(id),
 payload bytea NOT NULL,
 expires_at timestamptz NOT NULL,
 used_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE document_signatures (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 document_id uuid NOT NULL REFERENCES documents(id),
 task_id uuid NOT NULL UNIQUE REFERENCES workflow_tasks(id),
 challenge_id uuid NOT NULL UNIQUE REFERENCES signing_challenges(id),
 signer_id uuid NOT NULL REFERENCES users(id),
 signer_iin text NOT NULL,
 signer_name text NOT NULL,
 certificate_serial text NOT NULL,
 cms bytea NOT NULL,
 payload bytea NOT NULL,
 payload_sha256 text NOT NULL,
 verification jsonb NOT NULL,
 verified_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX document_signatures_document_idx ON document_signatures(document_id);
