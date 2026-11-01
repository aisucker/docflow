ALTER TABLE documents ADD COLUMN priority text NOT NULL DEFAULT 'normal' CHECK(priority IN ('low','normal','high','urgent'));
ALTER TABLE documents ADD COLUMN tags text[] NOT NULL DEFAULT '{}';
CREATE INDEX documents_tags_idx ON documents USING gin(tags);
CREATE INDEX documents_due_idx ON documents(due_at,id) WHERE deleted_at IS NULL AND due_at IS NOT NULL;
CREATE TABLE document_favorites(user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,document_id));
CREATE TABLE saved_document_views(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,name text NOT NULL,filters jsonb NOT NULL CHECK(jsonb_typeof(filters)='object'),created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(user_id,name));
CREATE TABLE document_comments(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,author_id uuid NOT NULL REFERENCES users(id),body text NOT NULL,mention_ids uuid[] NOT NULL DEFAULT '{}',created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX document_comments_doc_idx ON document_comments(document_id,created_at);
CREATE TABLE document_checklist(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,label text NOT NULL,done boolean NOT NULL DEFAULT false,created_by uuid NOT NULL REFERENCES users(id),completed_by uuid REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz);
CREATE INDEX document_checklist_doc_idx ON document_checklist(document_id,created_at);
CREATE TABLE document_links(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),source_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,target_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,kind text NOT NULL CHECK(kind IN ('related','basis','response_to')),created_by uuid NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now(),CHECK(source_id<>target_id),UNIQUE(source_id,target_id,kind));
CREATE INDEX document_links_target_idx ON document_links(target_id);
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check CHECK(kind IN ('task_assigned','task_delegated','task_completed','document_returned','document_rejected','document_completed','due_soon','overdue','mention'));

CREATE TABLE document_revisions(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,version integer NOT NULL,editor_id uuid REFERENCES users(id),snapshot jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(document_id,version));
CREATE FUNCTION docjet_card_snapshot(d documents) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$ SELECT jsonb_build_object('title',d.title,'summary',d.summary,'fields',d.fields,'assignee_id',d.assignee_id,'department_id',d.department_id,'due_at',d.due_at,'priority',d.priority,'tags',d.tags,'registration_number',d.registration_number,'project_number',d.project_number); $$;
INSERT INTO document_revisions(document_id,version,snapshot) SELECT id,1,docjet_card_snapshot(d) FROM documents d;
CREATE FUNCTION docjet_capture_card_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor uuid;next_version integer;card jsonb;
BEGIN
 card:=docjet_card_snapshot(NEW);
 IF TG_OP='UPDATE' AND card=docjet_card_snapshot(OLD) THEN RETURN NEW; END IF;
 actor:=nullif(current_setting('docjet.actor_id',true),'')::uuid;
 IF TG_OP='INSERT' AND actor IS NULL THEN actor:=NEW.author_id; END IF;
 SELECT coalesce(max(version),0)+1 INTO next_version FROM document_revisions WHERE document_id=NEW.id;
 INSERT INTO document_revisions(document_id,version,editor_id,snapshot) VALUES(NEW.id,next_version,actor,card);
 RETURN NEW;
END; $$;
CREATE TRIGGER document_card_revision AFTER INSERT OR UPDATE OF title,summary,fields,assignee_id,department_id,due_at,priority,tags,registration_number,project_number ON documents FOR EACH ROW EXECUTE FUNCTION docjet_capture_card_revision();
