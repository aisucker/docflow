package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type documentInput struct {
	TypeID             string         `json:"type_id"`
	Title              string         `json:"title"`
	Summary            string         `json:"summary"`
	RegistrationNumber string         `json:"registration_number"`
	AssigneeID         string         `json:"assignee_id"`
	DepartmentID       string         `json:"department_id"`
	DueAt              *time.Time     `json:"due_at"`
	Fields             map[string]any `json:"fields"`
}

func validateValues(defs []FieldDef, fields map[string]any) string {
	known := map[string]FieldDef{}
	for _, f := range defs {
		known[f.Key] = f
	}
	for key, v := range fields {
		f, ok := known[key]
		if !ok {
			return "Неизвестное поле: " + key
		}
		if v == nil {
			continue
		}
		switch f.Type {
		case "string", "text", "date", "select":
			s, ok := v.(string)
			if !ok {
				return "Поле " + key + " должно быть строкой"
			}
			if f.Type == "date" && s != "" {
				if _, err := time.Parse("2006-01-02", s); err != nil {
					return "Неверная дата: " + key
				}
			}
			if f.Type == "select" {
				allowed := false
				for _, o := range f.Options {
					if s == o {
						allowed = true
					}
				}
				if !allowed {
					return "Недопустимое значение: " + key
				}
			}
		case "number":
			if _, ok := v.(float64); !ok {
				return "Поле " + key + " должно быть числом"
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return "Поле " + key + " должно быть логическим"
			}
		}
	}
	for _, f := range defs {
		v, ok := fields[f.Key]
		if f.Required && (!ok || v == nil || v == "") {
			return "Обязательное поле: " + f.Label
		}
	}
	return ""
}
func (s *Server) checkDocFields(ctx context.Context, typeID string, fields map[string]any) string {
	var raw []byte
	var active bool
	err := s.db.QueryRow(ctx, "SELECT fields,active FROM document_types WHERE id=$1", typeID).Scan(&raw, &active)
	if err != nil || !active {
		return "Тип документа не найден или отключён"
	}
	var defs []FieldDef
	if json.Unmarshal(raw, &defs) != nil {
		return "Повреждена схема типа документа"
	}
	return validateValues(defs, fields)
}
func (s *Server) readAllowed(ctx context.Context, docID string, p Principal) bool {
	if p.Role == "admin" || p.Role == "registrar" {
		var exists bool
		_ = s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1 AND deleted_at IS NULL)", docID).Scan(&exists)
		return exists
	}
	var allowed bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM documents d WHERE d.id=$1 AND d.deleted_at IS NULL AND (d.author_id=$2 OR d.assignee_id=$2 OR EXISTS(SELECT 1 FROM workflow_tasks t WHERE t.document_id=d.id AND t.assignee_id=$2)))`, docID, p.ID).Scan(&allowed)
	return allowed
}
func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	p := me(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	status := r.URL.Query().Get("status")
	typ := r.URL.Query().Get("type_id")
	journal := r.URL.Query().Get("journal")
	limit := 50
	if n, _ := strconv.Atoi(r.URL.Query().Get("limit")); n > 0 && n <= 100 {
		limit = n
	}
	args := []any{p.ID, q, status, typ, limit, p.Role, journal}
	rows, err := s.db.Query(r.Context(), `SELECT d.id,d.type_id,dt.name,d.title,d.summary,d.status,coalesce(d.registration_number,''),d.author_id,u.full_name,d.created_at,d.updated_at,d.due_at
 FROM documents d JOIN document_types dt ON dt.id=d.type_id JOIN users u ON u.id=d.author_id
 WHERE d.deleted_at IS NULL AND ($6 IN ('admin','registrar') OR d.author_id=$1 OR d.assignee_id=$1 OR EXISTS(SELECT 1 FROM workflow_tasks t WHERE t.document_id=d.id AND t.assignee_id=$1))
 AND ($2='' OR d.title ILIKE '%'||$2||'%' OR d.summary ILIKE '%'||$2||'%' OR d.registration_number ILIKE '%'||$2||'%')
 AND ($3='' OR d.status=$3) AND ($4='' OR d.type_id::text=$4)
 AND ($7<> 'mine' OR d.author_id=$1) AND ($7<> 'inbox' OR EXISTS(SELECT 1 FROM workflow_tasks t WHERE t.document_id=d.id AND t.assignee_id=$1 AND t.status IN ('pending','in_progress')))
 ORDER BY d.created_at DESC LIMIT $5`, args...)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, typeID, typeName, title, summary, st, num, author, authorName string
		var created, updated time.Time
		var due *time.Time
		if e := rows.Scan(&id, &typeID, &typeName, &title, &summary, &st, &num, &author, &authorName, &created, &updated, &due); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "type_id": typeID, "type_name": typeName, "title": title, "summary": summary, "status": st, "registration_number": num, "author_id": author, "author_name": authorName, "created_at": created, "updated_at": updated, "due_at": due})
	}
	respond(w, 200, out)
}
func (s *Server) createDocument(w http.ResponseWriter, r *http.Request) {
	var v documentInput
	if !decode(w, r, &v) {
		return
	}
	if strings.TrimSpace(v.Title) == "" || !goodID(v.TypeID) {
		fail(w, 400, "Укажите название и тип документа")
		return
	}
	if v.Fields == nil {
		v.Fields = map[string]any{}
	}
	if e := s.checkDocFields(r.Context(), v.TypeID, v.Fields); e != "" {
		fail(w, 400, e)
		return
	}
	b, _ := json.Marshal(v.Fields)
	p := me(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO documents(type_id,title,summary,registration_number,author_id,assignee_id,department_id,due_at,fields) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, v.TypeID, v.Title, v.Summary, nullableStr(v.RegistrationNumber), p.ID, nullableStr(v.AssigneeID), nullableStr(v.DepartmentID), nullableTime(v.DueAt), b).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	if err = event(r.Context(), tx, id, p.ID, "created", map[string]any{"title": v.Title}); err != nil {
		errDB(w, err)
		return
	}
	if err = emit(r.Context(), tx, "document.created", map[string]any{"document_id": id}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	var v struct {
		ID, TypeID, TypeName, Title, Summary, Status, AuthorID, AuthorName, RegistrationNumber, AssigneeID, DepartmentID string
		Fields                                                                                                           json.RawMessage
		DueAt, RegisteredAt                                                                                              *time.Time
		CreatedAt, UpdatedAt                                                                                             time.Time
	}
	err := s.db.QueryRow(r.Context(), `SELECT d.id,d.type_id,dt.name,d.title,d.summary,d.status,d.author_id,u.full_name,coalesce(d.registration_number,''),coalesce(d.assignee_id::text,''),coalesce(d.department_id::text,''),d.fields,d.due_at,d.registered_at,d.created_at,d.updated_at FROM documents d JOIN document_types dt ON dt.id=d.type_id JOIN users u ON u.id=d.author_id WHERE d.id=$1 AND d.deleted_at IS NULL`, id).Scan(&v.ID, &v.TypeID, &v.TypeName, &v.Title, &v.Summary, &v.Status, &v.AuthorID, &v.AuthorName, &v.RegistrationNumber, &v.AssigneeID, &v.DepartmentID, &v.Fields, &v.DueAt, &v.RegisteredAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]any{"id": v.ID, "type_id": v.TypeID, "type_name": v.TypeName, "title": v.Title, "summary": v.Summary, "status": v.Status, "author_id": v.AuthorID, "author_name": v.AuthorName, "registration_number": v.RegistrationNumber, "assignee_id": v.AssigneeID, "department_id": v.DepartmentID, "fields": v.Fields, "due_at": v.DueAt, "registered_at": v.RegisteredAt, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt})
}
func (s *Server) updateDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := me(r)
	var v documentInput
	if !decode(w, r, &v) {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var author, st, typeID string
	err = tx.QueryRow(r.Context(), "SELECT author_id,status,type_id FROM documents WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&author, &st, &typeID)
	if err != nil {
		errDB(w, err)
		return
	}
	if author != p.ID && p.Role != "admin" {
		fail(w, 403, "Редактирование доступно автору")
		return
	}
	if st != "draft" && st != "returned" && st != "rejected" {
		fail(w, 409, "Документ нельзя редактировать на согласовании или после утверждения")
		return
	}
	if strings.TrimSpace(v.Title) == "" {
		fail(w, 400, "Укажите название")
		return
	}
	if v.Fields == nil {
		v.Fields = map[string]any{}
	}
	if msg := s.checkDocFields(r.Context(), typeID, v.Fields); msg != "" {
		fail(w, 400, msg)
		return
	}
	b, _ := json.Marshal(v.Fields)
	_, err = tx.Exec(r.Context(), `UPDATE documents SET title=$1,summary=$2,registration_number=$3,assignee_id=$4,department_id=$5,due_at=$6,fields=$7,updated_at=now() WHERE id=$8`, v.Title, v.Summary, nullableStr(v.RegistrationNumber), nullableStr(v.AssigneeID), nullableStr(v.DepartmentID), nullableTime(v.DueAt), b, id)
	if err != nil {
		errDB(w, err)
		return
	}
	if err = event(r.Context(), tx, id, p.ID, "updated", map[string]any{"title": v.Title}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	p := me(r)
	if !mustRole(w, p, "admin") {
		return
	}
	id := r.PathValue("id")
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var active bool
	err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE document_id=$1 AND status='active')", id).Scan(&active)
	if err != nil {
		errDB(w, err)
		return
	}
	if active {
		fail(w, 409, "Нельзя удалить документ с активным маршрутом")
		return
	}
	tag, err := tx.Exec(r.Context(), "UPDATE documents SET deleted_at=now(),deleted_by=$1 WHERE id=$2 AND deleted_at IS NULL", p.ID, id)
	if err != nil {
		errDB(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "Документ не найден")
		return
	}
	if err = event(r.Context(), tx, id, p.ID, "deleted", map[string]any{}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) documentHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT e.id,e.kind,e.detail,e.occurred_at,coalesce(u.full_name,'Система') FROM document_events e LEFT JOIN users u ON e.actor_id=u.id WHERE document_id=$1 ORDER BY occurred_at,id`, id)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var eid int64
		var kind, name string
		var detail json.RawMessage
		var when time.Time
		if e := rows.Scan(&eid, &kind, &detail, &when, &name); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": eid, "kind": kind, "detail": detail, "occurred_at": when, "actor_name": name})
	}
	respond(w, 200, out)
}
func (s *Server) documentRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT t.id,t.step_index,t.step_id,t.title,t.status,t.comment,t.created_at,t.decided_at,u.full_name,t.assignee_id,wi.definition_snapshot,t.action_label FROM workflow_tasks t JOIN workflow_instances wi ON wi.id=t.instance_id JOIN users u ON t.assignee_id=u.id WHERE t.document_id=$1 ORDER BY t.created_at,t.step_index`, id)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var taskID, stepID, title, st, comment, name, assigneeID, actionLabel string
		var snapshot []byte
		var idx int
		var created time.Time
		var decided *time.Time
		if e := rows.Scan(&taskID, &idx, &stepID, &title, &st, &comment, &created, &decided, &name, &assigneeID, &snapshot, &actionLabel); e != nil {
			errDB(w, e)
			return
		}
		var flow Flow
		if err = json.Unmarshal(snapshot, &flow); err != nil {
			errDB(w, err)
			return
		}
		var step Step
		if idx >= 0 && idx < len(flow.Steps) {
			step = flow.Steps[idx]
		}
		actions := stepButtons(step)
		if st == "in_progress" {
			filtered := []WorkflowButton{}
			for _, a := range actions {
				if a.Effect != "start" {
					filtered = append(filtered, a)
				}
			}
			actions = filtered
		}
		out = append(out, map[string]any{"id": taskID, "step_index": idx, "step_id": stepID, "title": title, "status": st, "comment": comment, "created_at": created, "decided_at": decided, "assignee_name": name, "assignee_id": assigneeID, "actions": actions, "kind": step.Kind, "instructions": step.Instructions, "action_label": actionLabel})
	}
	respond(w, 200, out)
}

func (s *Server) trash(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	rows, err := s.db.Query(r.Context(), "SELECT d.id,d.title,d.status,d.deleted_at,u.full_name FROM documents d JOIN users u ON u.id=d.author_id WHERE d.deleted_at IS NOT NULL ORDER BY d.deleted_at DESC LIMIT 200")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, title, status, author string
		var when time.Time
		if e := rows.Scan(&id, &title, &status, &when, &author); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "title": title, "status": status, "author_name": author, "deleted_at": when})
	}
	respond(w, 200, out)
}
func (s *Server) restoreDocument(w http.ResponseWriter, r *http.Request) {
	p := me(r)
	if !mustRole(w, p, "admin") {
		return
	}
	id := r.PathValue("id")
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE documents SET deleted_at=NULL,deleted_by=NULL,updated_at=now() WHERE id=$1 AND deleted_at IS NOT NULL", id)
	if err != nil {
		errDB(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "Документ не найден в корзине")
		return
	}
	if err = event(r.Context(), tx, id, p.ID, "restored", map[string]any{}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
