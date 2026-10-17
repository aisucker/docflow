package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}
type Step struct {
	ParallelGroup string           `json:"parallel_group,omitempty"`
	Assignees     []AssigneeSource `json:"assignees,omitempty"`
	Kind          string           `json:"kind,omitempty"`
	Instructions  string           `json:"instructions,omitempty"`
	ButtonCodes   []string         `json:"button_codes,omitempty"`
	Actions       []WorkflowButton `json:"actions,omitempty"`
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	Mode          string           `json:"mode"`
	AssigneeIDs   []string         `json:"assignee_ids"`
	Condition     *Condition       `json:"condition,omitempty"`
}
type Flow struct {
	CompletionStatus string `json:"completion_status,omitempty"`
	Steps            []Step `json:"steps"`
}
type flowInput struct {
	Key        string `json:"workflow_key"`
	Name       string `json:"name"`
	DocTypeID  string `json:"doc_type_id"`
	Definition Flow   `json:"definition"`
}

func validateFlow(f Flow) string {
	if len(f.Steps) == 0 || len(f.Steps) > 40 {
		return "Маршрут должен содержать 1–40 этапов"
	}
	if f.CompletionStatus != "" && f.CompletionStatus != "approved" && f.CompletionStatus != "completed" {
		return "Некорректный итоговый статус"
	}
	seen := map[string]bool{}
	groupsClosed := map[string]bool{}
	previousGroup := ""
	for _, step := range f.Steps {
		if !codePattern.MatchString(step.ID) || seen[step.ID] || strings.TrimSpace(step.Title) == "" {
			return "Некорректный идентификатор или название этапа"
		}
		seen[step.ID] = true
		if step.ParallelGroup != "" && !codePattern.MatchString(step.ParallelGroup) {
			return "Некорректный код параллельного блока"
		}
		if step.ParallelGroup != previousGroup {
			if previousGroup != "" {
				groupsClosed[previousGroup] = true
			}
			if groupsClosed[step.ParallelGroup] {
				return "Этапы одного параллельного блока должны идти подряд"
			}
			previousGroup = step.ParallelGroup
		}
		switch step.Kind {
		case "", "approval", "task", "acknowledgement", "registration":
		default:
			return "Неизвестный тип этапа"
		}
		if step.Mode != "all" && step.Mode != "any" {
			return "Режим этапа должен быть all или any"
		}
		if len(step.Assignees) == 0 && len(step.AssigneeIDs) == 0 {
			return "Укажите источник исполнителей для этапа «" + step.Title + "»"
		}
		if len(step.Assignees) > 20 || len(step.AssigneeIDs) > 30 {
			return "Слишком много источников исполнителей"
		}
		for _, src := range step.Assignees {
			switch src.Kind {
			case "user", "group":
				if !goodID(src.ID) {
					return "Некорректный пользователь или группа"
				}
			case "document_field":
				if !codePattern.MatchString(src.ID) {
					return "Некорректный код поля пользователя"
				}
			case "author", "assignee":
				if src.ID != "" {
					return "Источник автора или ответственного не требует кода"
				}
			default:
				return "Неизвестный источник исполнителей"
			}
		}
		ids := map[string]bool{}
		for _, id := range step.AssigneeIDs {
			if !goodID(id) || ids[id] {
				return "Некорректный или повторяющийся исполнитель"
			}
			ids[id] = true
		}
		if c := step.Condition; c != nil {
			if c.Field == "" {
				return "Не указано поле условия"
			}
			switch c.Operator {
			case "eq", "neq", "gt", "gte", "lt", "lte", "contains":
			default:
				return "Недопустимый оператор условия"
			}
		}
	}
	return ""
}
func conditionMatches(c *Condition, doc map[string]any) bool {
	if c == nil {
		return true
	}
	value, ok := doc[c.Field]
	if !ok {
		return false
	}
	switch c.Operator {
	case "eq":
		return reflect.DeepEqual(value, c.Value)
	case "neq":
		return !reflect.DeepEqual(value, c.Value)
	case "contains":
		a, ok := value.(string)
		b, ok2 := c.Value.(string)
		return ok && ok2 && strings.Contains(strings.ToLower(a), strings.ToLower(b))
	default:
		a, ok := value.(float64)
		b, ok2 := c.Value.(float64)
		if !ok || !ok2 {
			return false
		}
		switch c.Operator {
		case "gt":
			return a > b
		case "gte":
			return a >= b
		case "lt":
			return a < b
		case "lte":
			return a <= b
		}
	}
	return false
}
func (s *Server) validateAssignees(ctx context.Context, flow Flow) string {
	if msg := s.validateSourceRefs(ctx, flow); msg != "" {
		return msg
	}
	ids := map[string]bool{}
	for _, st := range flow.Steps {
		for _, id := range st.AssigneeIDs {
			ids[id] = true
		}
	}
	for id := range ids {
		var active bool
		if err := s.db.QueryRow(ctx, "SELECT active FROM users WHERE id=$1", id).Scan(&active); err != nil || !active {
			return "Исполнитель отсутствует или заблокирован: " + id
		}
	}
	return ""
}
func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT wd.id,wd.workflow_key,wd.version,wd.name,coalesce(wd.doc_type_id::text,''),wd.active,wd.created_at FROM workflow_definitions wd WHERE wd.active=true ORDER BY wd.name,wd.version DESC`)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, key, name, typ string
		var version int
		var active bool
		var created time.Time
		if e := rows.Scan(&id, &key, &version, &name, &typ, &active, &created); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "workflow_key": key, "version": version, "name": name, "doc_type_id": typ, "active": active, "created_at": created})
	}
	respond(w, 200, out)
}
func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	var id, key, name, typ string
	var version int
	var def json.RawMessage
	err := s.db.QueryRow(r.Context(), "SELECT id,workflow_key,version,name,coalesce(doc_type_id::text,''),definition FROM workflow_definitions WHERE id=$1", r.PathValue("id")).Scan(&id, &key, &version, &name, &typ, &def)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]any{"id": id, "workflow_key": key, "version": version, "name": name, "doc_type_id": typ, "definition": def})
}
func (s *Server) createWorkflow(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v flowInput
	if !decode(w, r, &v) {
		return
	}
	if !codePattern.MatchString(v.Key) || strings.TrimSpace(v.Name) == "" {
		fail(w, 400, "Название и код маршрута обязательны")
		return
	}
	if msg := validateFlow(v.Definition); msg != "" {
		fail(w, 400, msg)
		return
	}
	if msg := s.prepareFlow(r.Context(), &v.Definition); msg != "" {
		fail(w, 400, msg)
		return
	}
	if msg := s.validateAssignees(r.Context(), v.Definition); msg != "" {
		fail(w, 400, msg)
		return
	}
	b, _ := json.Marshal(v.Definition)
	var id string
	err := s.db.QueryRow(r.Context(), "INSERT INTO workflow_definitions(workflow_key,version,name,doc_type_id,definition,created_by) VALUES($1,1,$2,$3,$4,$5) RETURNING id", strings.TrimSpace(v.Key), strings.TrimSpace(v.Name), nullableStr(v.DocTypeID), b, me(r).ID).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) updateWorkflow(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v struct {
		Name       string `json:"name"`
		Definition Flow   `json:"definition"`
	}
	if !decode(w, r, &v) {
		return
	}
	if strings.TrimSpace(v.Name) == "" {
		fail(w, 400, "Укажите название")
		return
	}
	if msg := validateFlow(v.Definition); msg != "" {
		fail(w, 400, msg)
		return
	}
	if msg := s.prepareFlow(r.Context(), &v.Definition); msg != "" {
		fail(w, 400, msg)
		return
	}
	if msg := s.validateAssignees(r.Context(), v.Definition); msg != "" {
		fail(w, 400, msg)
		return
	}
	b, _ := json.Marshal(v.Definition)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var key, typ string
	var version int
	// Lock row serializes route version creation, without changing existing historical definitions.
	err = tx.QueryRow(r.Context(), "SELECT workflow_key,coalesce(doc_type_id::text,''),version FROM workflow_definitions WHERE id=$1 FOR UPDATE", r.PathValue("id")).Scan(&key, &typ, &version)
	if err != nil {
		errDB(w, err)
		return
	}
	var newest int
	err = tx.QueryRow(r.Context(), "SELECT max(version) FROM workflow_definitions WHERE workflow_key=$1", key).Scan(&newest)
	if err != nil {
		errDB(w, err)
		return
	}
	// An administrator always edits the latest version, avoiding stale branching.
	if version != newest {
		fail(w, 409, "Откройте последнюю версию маршрута")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE workflow_definitions SET active=false WHERE workflow_key=$1", key); err != nil {
		errDB(w, err)
		return
	}
	var id string
	err = tx.QueryRow(r.Context(), "INSERT INTO workflow_definitions(workflow_key,version,name,doc_type_id,definition,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id", key, newest+1, v.Name, nullableStr(typ), b, me(r).ID).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]any{"id": id, "version": newest + 1})
}
func (s *Server) startWorkflow(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("id")
	p := me(r)
	var in struct {
		WorkflowID string `json:"workflow_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var author, st, typ, title, workflowKey string
	var fieldsRaw []byte
	err = tx.QueryRow(r.Context(), "SELECT d.author_id,d.status,d.type_id,d.title,d.fields,coalesce(d.workflow_key,dt.default_workflow_key,'') FROM documents d JOIN document_types dt ON dt.id=d.type_id WHERE d.id=$1 AND d.deleted_at IS NULL FOR UPDATE OF d", docID).Scan(&author, &st, &typ, &title, &fieldsRaw, &workflowKey)
	if err != nil {
		errDB(w, err)
		return
	}
	if author != p.ID && p.Role != "admin" && p.Role != "registrar" {
		fail(w, 403, "Запускать маршрут может автор или регистратор")
		return
	}
	if st != "draft" && st != "returned" && st != "rejected" {
		fail(w, 409, "Маршрут уже запущен или завершён")
		return
	}
	if workflowKey == "" {
		fail(w, 409, "Для типа документа не настроен маршрут по умолчанию")
		return
	}
	var defRaw []byte
	var definitionID string
	var flowType string
	var active bool
	err = tx.QueryRow(r.Context(), "SELECT id,definition,coalesce(doc_type_id::text,''),active FROM workflow_definitions WHERE workflow_key=$1 AND active=true ORDER BY version DESC LIMIT 1", workflowKey).Scan(&definitionID, &defRaw, &flowType, &active)
	if err != nil {
		errDB(w, err)
		return
	}
	if !active || (flowType != "" && flowType != typ) {
		fail(w, 400, "Маршрут неактивен или не соответствует типу документа")
		return
	}
	var f Flow
	if err = json.Unmarshal(defRaw, &f); err != nil {
		fail(w, 500, "Повреждено определение маршрута")
		return
	}
	if msg := s.validateAssignees(r.Context(), f); msg != "" {
		fail(w, 400, msg)
		return
	}
	var instance string
	err = tx.QueryRow(r.Context(), "INSERT INTO workflow_instances(document_id,definition_id,definition_snapshot,started_by) VALUES($1,$2,$3,$4) RETURNING id", docID, definitionID, defRaw, p.ID).Scan(&instance)
	if err != nil {
		errDB(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE documents SET status='in_review',updated_at=now() WHERE id=$1", docID); err != nil {
		errDB(w, err)
		return
	}
	if err = event(r.Context(), tx, docID, p.ID, "workflow.started", map[string]any{"instance_id": instance}); err != nil {
		errDB(w, err)
		return
	}
	if err = s.advance(r.Context(), tx, instance, docID, 0, f, fieldsRaw, title); err != nil {
		var invalid routeError
		if errors.As(err, &invalid) {
			fail(w, 409, string(invalid))
		} else {
			errDB(w, err)
		}
		return
	}
	if err = emit(r.Context(), tx, "workflow.started", map[string]any{"document_id": docID, "instance_id": instance}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"instance_id": instance})
}
func (s *Server) advance(ctx context.Context, tx pgx.Tx, instance, docID string, from int, f Flow, fieldsRaw []byte, title string) error {
	return s.activate(ctx, tx, instance, docID, from, f, fieldsRaw, title)
}
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	p := me(r)
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	rows, err := s.db.Query(r.Context(), `SELECT t.id,t.document_id,d.title,t.step_index,t.title,t.status,t.created_at,u.full_name,d.due_at FROM workflow_tasks t JOIN documents d ON d.id=t.document_id JOIN users u ON t.assignee_id=u.id WHERE t.assignee_id=$1 AND (t.status=$2 OR ($2='pending' AND t.status='in_progress')) AND d.deleted_at IS NULL ORDER BY d.due_at ASC NULLS LAST,t.created_at DESC LIMIT 100`, p.ID, status)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, docID, docTitle, taskTitle, st, user string
		var idx int
		var created time.Time
		var due *time.Time
		if e := rows.Scan(&id, &docID, &docTitle, &idx, &taskTitle, &st, &created, &user, &due); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "document_id": docID, "document_title": docTitle, "step_index": idx, "title": taskTitle, "status": st, "created_at": created, "due_at": due, "assignee_name": user})
	}
	respond(w, 200, out)
}
func (s *Server) decideTask(w http.ResponseWriter, r *http.Request) {
	p := me(r)
	var in struct {
		Decision   string `json:"decision"`
		ActionCode string `json:"action_code"`
		Comment    string `json:"comment"`
	}
	if !decode(w, r, &in) {
		return
	}
	taskID := r.PathValue("id")
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var instance string
	err = tx.QueryRow(r.Context(), "SELECT instance_id FROM workflow_tasks WHERE id=$1", taskID).Scan(&instance)
	if err != nil {
		errDB(w, err)
		return
	}
	var docID, instStatus, title string
	var stepIdx int
	var defRaw, fieldsRaw []byte
	// Every decision locks the instance; concurrent approvals cannot advance a stage twice.
	err = tx.QueryRow(r.Context(), "SELECT wi.document_id,wi.status,wi.current_step,wi.definition_snapshot,d.fields,d.title FROM workflow_instances wi JOIN documents d ON d.id=wi.document_id WHERE wi.id=$1 FOR UPDATE OF wi", instance).Scan(&docID, &instStatus, &stepIdx, &defRaw, &fieldsRaw, &title)
	if err != nil {
		errDB(w, err)
		return
	}
	var userID, taskStatus, delegatedFrom string
	var taskStep int
	err = tx.QueryRow(r.Context(), "SELECT assignee_id,status,step_index,coalesce(delegated_from::text,'') FROM workflow_tasks WHERE id=$1 FOR UPDATE", taskID).Scan(&userID, &taskStatus, &taskStep, &delegatedFrom)
	if err != nil {
		errDB(w, err)
		return
	}
	if instStatus != "active" || (taskStatus != "pending" && taskStatus != "in_progress") {
		fail(w, 409, "Задача уже обработана или этап завершён")
		return
	}
	if p.ID != userID {
		fail(w, 403, "Вы не назначены исполнителем")
		return
	}
	var flow Flow
	if err = json.Unmarshal(defRaw, &flow); err != nil {
		errDB(w, err)
		return
	}
	if stepIdx < 0 || stepIdx >= len(flow.Steps) {
		fail(w, 500, "Некорректное состояние маршрута")
		return
	}
	groupEnd := parallelEnd(flow.Steps, stepIdx)
	if taskStep < stepIdx || taskStep >= groupEnd {
		fail(w, 409, "Задача не относится к текущему этапу")
		return
	}
	code := in.ActionCode
	if code == "" {
		code = in.Decision
	}
	var action *WorkflowButton
	for _, b := range stepButtons(flow.Steps[taskStep]) {
		if b.Code == code {
			chosen := b
			action = &chosen
			break
		}
	}
	if action == nil {
		fail(w, 400, "Эта кнопка недоступна на текущем этапе")
		return
	}
	if (action.CommentRequired || action.Effect == "return" || action.Effect == "reject") && strings.TrimSpace(in.Comment) == "" {
		fail(w, 400, "Для этого действия требуется комментарий")
		return
	}
	status := ""
	switch action.Effect {
	case "complete":
		status = "completed"
		if flow.Steps[taskStep].Kind == "" || flow.Steps[taskStep].Kind == "approval" {
			status = "approved"
		}
	case "return":
		status = "returned"
	case "reject":
		status = "rejected"
	case "start":
		if taskStatus != "pending" {
			fail(w, 409, "Задача уже принята в работу")
			return
		}
		if _, err = tx.Exec(r.Context(), "UPDATE workflow_tasks SET status='in_progress',comment=$2,action_code=$3,action_label=$4 WHERE id=$1", taskID, in.Comment, action.Code, action.Label); err != nil {
			errDB(w, err)
			return
		}
		if err = event(r.Context(), tx, docID, p.ID, "workflow.task_started", map[string]any{"task_id": taskID, "action_code": action.Code, "action_label": action.Label, "comment": in.Comment}); err != nil {
			errDB(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			errDB(w, err)
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
		return
	default:
		fail(w, 500, "Некорректное действие в маршруте")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE workflow_tasks SET status=$1,comment=$2,decided_at=now(),action_code=$4,action_label=$5 WHERE id=$3", status, in.Comment, taskID, action.Code, action.Label); err != nil {
		errDB(w, err)
		return
	}
	// The delegator and delegate represent one obligation. The first decision wins.
	if delegatedFrom != "" {
		_, err = tx.Exec(r.Context(), "UPDATE workflow_tasks SET status='cancelled',decided_at=now(),action_label='Исполнено порученным сотрудником' WHERE id=$1 AND status IN ('pending','in_progress')", delegatedFrom)
	} else {
		_, err = tx.Exec(r.Context(), "UPDATE workflow_tasks SET status='cancelled',decided_at=now(),action_label='Исполнено основным исполнителем' WHERE delegated_from=$1 AND status IN ('pending','in_progress')", taskID)
	}
	if err != nil {
		errDB(w, err)
		return
	}
	if err = event(r.Context(), tx, docID, p.ID, "workflow.task_"+status, map[string]any{"task_id": taskID, "comment": in.Comment, "action_code": action.Code, "action_label": action.Label}); err != nil {
		errDB(w, err)
		return
	}
	if action.Effect != "complete" {
		if _, err = tx.Exec(r.Context(), "UPDATE workflow_tasks SET status='cancelled' WHERE instance_id=$1 AND step_index >= $2 AND step_index < $3 AND status IN ('pending','in_progress')", instance, stepIdx, groupEnd); err != nil {
			errDB(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), "UPDATE workflow_instances SET status=$1,completed_at=now() WHERE id=$2", status, instance); err != nil {
			errDB(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), "UPDATE documents SET status=$1,updated_at=now() WHERE id=$2", status, docID); err != nil {
			errDB(w, err)
			return
		}
		if err = emit(r.Context(), tx, "workflow."+status, map[string]any{"document_id": docID}); err != nil {
			errDB(w, err)
			return
		}
	} else {
		isDone, e := s.groupDone(r.Context(), tx, instance, flow, stepIdx)
		if e != nil {
			errDB(w, e)
			return
		}
		if isDone {
			if err = s.advance(r.Context(), tx, instance, docID, groupEnd, flow, fieldsRaw, title); err != nil {
				var invalid routeError
				if errors.As(err, &invalid) {
					fail(w, 409, string(invalid))
				} else {
					errDB(w, err)
				}
				return
			}
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
