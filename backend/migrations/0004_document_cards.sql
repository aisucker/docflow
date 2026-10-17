ALTER TABLE document_types ADD COLUMN card_fields jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(card_fields) = 'array');
