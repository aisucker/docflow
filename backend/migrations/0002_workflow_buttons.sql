CREATE TABLE workflow_buttons (
 code text PRIMARY KEY,
 label text NOT NULL,
 effect text NOT NULL CHECK(effect IN ('complete','start','return','reject')),
 style text NOT NULL DEFAULT 'primary' CHECK(style IN ('primary','subtle','danger-outline')),
 comment_required boolean NOT NULL DEFAULT false,
 active boolean NOT NULL DEFAULT true
);
INSERT INTO workflow_buttons(code,label,effect,style,comment_required) VALUES
 ('approve','Согласовать','complete','primary',false),
 ('complete','Выполнить','complete','primary',false),
 ('acknowledge','Ознакомлен','complete','primary',false),
 ('register','Зарегистрировано','complete','primary',false),
 ('start','Принять в работу','start','subtle',false),
 ('return','Вернуть на доработку','return','subtle',true),
 ('reject','Отклонить','reject','danger-outline',true);
ALTER TABLE documents DROP CONSTRAINT documents_status_check;
ALTER TABLE documents ADD CONSTRAINT documents_status_check CHECK(status IN ('draft','in_review','in_progress','approved','completed','rejected','returned'));
ALTER TABLE workflow_instances DROP CONSTRAINT workflow_instances_status_check;
ALTER TABLE workflow_instances ADD CONSTRAINT workflow_instances_status_check CHECK(status IN ('active','approved','completed','rejected','returned'));
ALTER TABLE workflow_tasks DROP CONSTRAINT workflow_tasks_status_check;
ALTER TABLE workflow_tasks ADD CONSTRAINT workflow_tasks_status_check CHECK(status IN ('pending','in_progress','approved','completed','rejected','returned','cancelled'));
ALTER TABLE workflow_tasks ADD COLUMN action_code text NOT NULL DEFAULT '';
ALTER TABLE workflow_tasks ADD COLUMN action_label text NOT NULL DEFAULT '';
