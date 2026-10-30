CREATE TABLE pdf_exports (
 token_hash text PRIMARY KEY,
 document_id uuid NOT NULL REFERENCES documents(id),
 file_id uuid REFERENCES files(id),
 signature_ids jsonb NOT NULL,
 metadata jsonb NOT NULL,
 pdf_sha256 text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 checked_at timestamptz,
 check_valid boolean
);
CREATE INDEX pdf_exports_document ON pdf_exports(document_id);
