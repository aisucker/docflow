package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,48}$`)

type FieldDef struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

func checkFields(defs []FieldDef) string {
	if len(defs) > 60 {
		return "Не больше 60 полей"
	}
	used := map[string]bool{}
	for _, f := range defs {
		if !codePattern.MatchString(f.Key) || used[f.Key] || f.Label == "" {
			return "Некорректный код, повтор или название поля"
		}
		used[f.Key] = true
		switch f.Type {
		case "string", "text", "number", "boolean", "date", "select":
		default:
			return "Неизвестный тип поля"
		}
		if f.Type == "select" && len(f.Options) == 0 {
			return "Для списка требуются варианты"
		}
	}
	return ""
}
func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT u.id,u.email,u.full_name,u.role,u.active,coalesce(u.department_id::text,''),coalesce(u.position_id::text,'') FROM users u ORDER BY u.full_name`)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, email, name, role, dep, pos string
		var active bool
		if e := rows.Scan(&id, &email, &name, &role, &active, &dep, &pos); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "email": email, "full_name": name, "role": role, "active": active, "department_id": dep, "position_id": pos})
	}
	respond(w, 200, out)
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v struct {
		Email        string `json:"email"`
		FullName     string `json:"full_name"`
		Password     string `json:"password"`
		Role         string `json:"role"`
		DepartmentID string `json:"department_id"`
		PositionID   string `json:"position_id"`
	}
	if !decode(w, r, &v) {
		return
	}
	if len(v.Password) < 12 || !strings.Contains(v.Email, "@") || strings.TrimSpace(v.FullName) == "" {
		fail(w, 400, "Нужны имя, email и пароль от 12 символов")
		return
	}
	if v.Role != "admin" && v.Role != "registrar" && v.Role != "employee" {
		fail(w, 400, "Неизвестная роль")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.DefaultCost)
	var id string
	err := s.db.QueryRow(r.Context(), `INSERT INTO users(email,password_hash,full_name,role,department_id,position_id) VALUES(lower($1),$2,$3,$4,$5,$6) RETURNING id`, strings.TrimSpace(v.Email), string(hash), v.FullName, v.Role, nullableStr(v.DepartmentID), nullableStr(v.PositionID)).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) departments(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), "SELECT id,name,coalesce(parent_id::text,'') FROM departments ORDER BY name")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name, parent string
		if e := rows.Scan(&id, &name, &parent); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]string{"id": id, "name": name, "parent_id": parent})
	}
	respond(w, 200, out)
}
func (s *Server) createDepartment(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
	}
	if !decode(w, r, &v) {
		return
	}
	if strings.TrimSpace(v.Name) == "" {
		fail(w, 400, "Укажите название")
		return
	}
	var id string
	err := s.db.QueryRow(r.Context(), "INSERT INTO departments(name,parent_id) VALUES($1,$2) RETURNING id", v.Name, nullableStr(v.ParentID)).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) positions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), "SELECT id,name FROM positions ORDER BY name")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name string
		if e := rows.Scan(&id, &name); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]string{"id": id, "name": name})
	}
	respond(w, 200, out)
}
func (s *Server) createPosition(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &v) {
		return
	}
	var id string
	err := s.db.QueryRow(r.Context(), "INSERT INTO positions(name) VALUES($1) RETURNING id", strings.TrimSpace(v.Name)).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) documentTypes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), "SELECT id,code,name,fields,active FROM document_types ORDER BY name")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, code, name string
		var fields json.RawMessage
		var active bool
		if e := rows.Scan(&id, &code, &name, &fields, &active); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "code": code, "name": name, "fields": fields, "active": active})
	}
	respond(w, 200, out)
}

type docTypeInput struct {
	Code   string     `json:"code"`
	Name   string     `json:"name"`
	Fields []FieldDef `json:"fields"`
	Active *bool      `json:"active,omitempty"`
}

func (s *Server) createDocumentType(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v docTypeInput
	if !decode(w, r, &v) {
		return
	}
	if !codePattern.MatchString(v.Code) || strings.TrimSpace(v.Name) == "" {
		fail(w, 400, "Некорректный код или название")
		return
	}
	if msg := checkFields(v.Fields); msg != "" {
		fail(w, 400, msg)
		return
	}
	if v.Fields == nil {
		v.Fields = []FieldDef{}
	}
	b, _ := json.Marshal(v.Fields)
	var id string
	err := s.db.QueryRow(r.Context(), "INSERT INTO document_types(code,name,fields) VALUES($1,$2,$3) RETURNING id", v.Code, v.Name, b).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) updateDocumentType(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v docTypeInput
	if !decode(w, r, &v) {
		return
	}
	if strings.TrimSpace(v.Name) == "" {
		fail(w, 400, "Укажите название")
		return
	}
	if msg := checkFields(v.Fields); msg != "" {
		fail(w, 400, msg)
		return
	}
	if v.Fields == nil {
		v.Fields = []FieldDef{}
	}
	b, _ := json.Marshal(v.Fields)
	active := true
	if v.Active != nil {
		active = *v.Active
	}
	tag, err := s.db.Exec(r.Context(), "UPDATE document_types SET name=$1,fields=$2,active=$3 WHERE id=$4", v.Name, b, active, r.PathValue("id"))
	if err != nil {
		errDB(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "Тип не найден")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v struct {
		Role         string `json:"role"`
		Active       bool   `json:"active"`
		DepartmentID string `json:"department_id"`
		PositionID   string `json:"position_id"`
	}
	if !decode(w, r, &v) {
		return
	}
	if v.Role != "admin" && v.Role != "employee" && v.Role != "registrar" {
		fail(w, 400, "Некорректная роль")
		return
	}
	if r.PathValue("id") == me(r).ID && (!v.Active || v.Role != "admin") {
		fail(w, 409, "Нельзя отключить свою административную учётную запись")
		return
	}
	tag, err := s.db.Exec(r.Context(), "UPDATE users SET role=$1,active=$2,department_id=$3,position_id=$4 WHERE id=$5", v.Role, v.Active, nullableStr(v.DepartmentID), nullableStr(v.PositionID), r.PathValue("id"))
	if err != nil {
		errDB(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "Пользователь не найден")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
