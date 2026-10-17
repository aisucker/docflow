ALTER TABLE users ADD COLUMN preferences jsonb NOT NULL DEFAULT '{"compact":false,"start_page":"documents"}'::jsonb CHECK(jsonb_typeof(preferences)='object');
