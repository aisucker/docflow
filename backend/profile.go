package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type profilePreferences struct {
	Compact   bool   `json:"compact"`
	StartPage string `json:"start_page"`
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	p := me(r)
	var id, email, name, role, department, position string
	var joined time.Time
	var raw []byte
	err := s.db.QueryRow(r.Context(), `SELECT u.id,u.email,u.full_name,u.role,coalesce(d.name,''),coalesce(pos.name,''),u.created_at,u.preferences
		FROM users u LEFT JOIN departments d ON d.id=u.department_id LEFT JOIN positions pos ON pos.id=u.position_id
		WHERE u.id=$1`, p.ID).Scan(&id, &email, &name, &role, &department, &position, &joined, &raw)
	if err != nil {
		errDB(w, err)
		return
	}
	var preferences profilePreferences
	if err := json.Unmarshal(raw, &preferences); err != nil {
		errDB(w, err)
		return
	}
	if preferences.StartPage == "" {
		preferences.StartPage = "documents"
	}
	respond(w, 200, map[string]any{"id": id, "email": email, "full_name": name, "role": role, "department_name": department, "position_name": position, "joined_at": joined, "preferences": preferences})
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FullName string `json:"full_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.FullName = strings.TrimSpace(in.FullName)
	if in.FullName == "" || len([]rune(in.FullName)) > 120 {
		fail(w, 400, "Укажите имя длиной до 120 символов")
		return
	}
	if _, err := s.db.Exec(r.Context(), "UPDATE users SET full_name=$1 WHERE id=$2", in.FullName, me(r).ID); err != nil {
		errDB(w, err)
		return
	}
	s.profile(w, r)
}

func (s *Server) updateProfilePreferences(w http.ResponseWriter, r *http.Request) {
	var in profilePreferences
	if !decode(w, r, &in) {
		return
	}
	if in.StartPage != "documents" && in.StartPage != "tasks" && in.StartPage != "profile" {
		fail(w, 400, "Неизвестная стартовая страница")
		return
	}
	raw, _ := json.Marshal(in)
	if _, err := s.db.Exec(r.Context(), "UPDATE users SET preferences=$1 WHERE id=$2", raw, me(r).ID); err != nil {
		errDB(w, err)
		return
	}
	s.profile(w, r)
}

func (s *Server) profileOverview(w http.ResponseWriter, r *http.Request) {
	id := me(r).ID
	var pending, overdue, mine, drafts int
	err := s.db.QueryRow(r.Context(), `SELECT
		(SELECT count(*) FROM workflow_tasks t JOIN documents d ON d.id=t.document_id WHERE t.assignee_id=$1 AND t.status IN ('pending','in_progress') AND d.deleted_at IS NULL),
		(SELECT count(*) FROM workflow_tasks t JOIN documents d ON d.id=t.document_id WHERE t.assignee_id=$1 AND t.status IN ('pending','in_progress') AND d.deleted_at IS NULL AND d.due_at < now()),
		(SELECT count(*) FROM documents WHERE author_id=$1 AND deleted_at IS NULL),
		(SELECT count(*) FROM documents WHERE author_id=$1 AND status='draft' AND deleted_at IS NULL)`, id).Scan(&pending, &overdue, &mine, &drafts)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]int{"pending_tasks": pending, "overdue_tasks": overdue, "my_documents": mine, "drafts": drafts})
}
