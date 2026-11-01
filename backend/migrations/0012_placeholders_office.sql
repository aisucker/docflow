CREATE TABLE placeholder_tags (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), code text NOT NULL, name text NOT NULL,
 type_id uuid REFERENCES document_types(id), script text NOT NULL, active boolean NOT NULL DEFAULT true,
 version integer NOT NULL DEFAULT 1, created_by uuid REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX placeholder_scope_code ON placeholder_tags(code,coalesce(type_id::text,''));
INSERT INTO placeholder_tags(code,name,script) VALUES
 ('title','Название документа','return document.title;'),
 ('summary','Содержание','return document.summary;'),
 ('registration_number','Регистрационный номер','return document.registration_number;'),
 ('project_number','Проектный номер','return document.project_number;'),
 ('created_at','Дата создания','return helpers.date(document.created_at);'),
 ('author_name','Автор','return document.author.full_name;'),
 ('assignee_name','Ответственный','return document.assignee ? document.assignee.full_name : "";'),
 ('department_name','Подразделение','return document.department ? document.department.name : "";'),
 ('due_at','Срок исполнения','return helpers.date(document.due_at);');
ALTER TABLE templates ADD COLUMN version integer NOT NULL DEFAULT 1;
ALTER TABLE templates ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
CREATE TABLE office_settings (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 builder_path text NOT NULL DEFAULT '', docs_url text NOT NULL DEFAULT '', internal_url text NOT NULL DEFAULT '',
 storage_url text NOT NULL DEFAULT '', jwt_secret bytea, updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO office_settings(singleton) VALUES(true);
CREATE TABLE office_sessions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), template_id uuid NOT NULL REFERENCES templates(id),
 user_id uuid NOT NULL REFERENCES users(id), document_key text NOT NULL,
 base_version integer NOT NULL, last_hash text NOT NULL DEFAULT '',
 expires_at timestamptz NOT NULL DEFAULT now()+interval '8 hours',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX office_sessions_template_idx ON office_sessions(template_id,expires_at);
