package main

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/jackc/pgx/v5"
)

type placeholderTag struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	TypeID    string    `json:"type_id"`
	Script    string    `json:"script"`
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

var tagCodePattern = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_.-]{0,79}$`)

func (s *Server) placeholderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/placeholder-tags", s.authorize(s.listPlaceholderTags))
	mux.HandleFunc("POST /api/v1/admin/placeholder-tags", s.authorize(s.savePlaceholderTag))
	mux.HandleFunc("PUT /api/v1/admin/placeholder-tags/{id}", s.authorize(s.savePlaceholderTag))
	mux.HandleFunc("POST /api/v1/admin/placeholder-tags/test", s.authorize(s.testPlaceholderTag))
	mux.HandleFunc("GET /api/v1/documents/{id}/placeholder-values", s.authorize(s.documentPlaceholderValues))
}
func (s *Server) loadPlaceholderTags(ctx context.Context, typeID string, all bool) ([]placeholderTag, error) {
	rows, err := s.db.Query(ctx, `SELECT id,code,name,coalesce(type_id::text,''),script,active,version,updated_at FROM placeholder_tags WHERE $1::boolean OR (active AND (type_id IS NULL OR type_id::text=$2)) ORDER BY code,type_id NULLS FIRST`, all, typeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []placeholderTag{}
	for rows.Next() {
		var v placeholderTag
		if err = rows.Scan(&v.ID, &v.Code, &v.Name, &v.TypeID, &v.Script, &v.Active, &v.Version, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Server) listPlaceholderTags(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	if all && !mustRole(w, me(r), "admin") {
		return
	}
	items, err := s.loadPlaceholderTags(r.Context(), r.URL.Query().Get("type_id"), all)
	if err != nil {
		errDB(w, err)
		return
	}
	if me(r).Role != "admin" {
		for i := range items {
			items[i].Script = ""
		}
	}
	respond(w, 200, items)
}
func validatePlaceholder(code, name, script string) error {
	if !tagCodePattern.MatchString(code) {
		return badWorkspace("Tag: 1–80 букв, цифр, точки, дефиса или подчёркивания; первый символ — буква")
	}
	if name == "" || len([]rune(name)) > 150 {
		return badWorkspace("Укажите название до 150 символов")
	}
	if len(script) == 0 || len(script) > 16<<10 {
		return badWorkspace("JavaScript должен содержать 1–16384 байта")
	}
	if _, err := goja.Compile("placeholder.js", "(function(document,helpers){\n"+script+"\n})", true); err != nil {
		return badWorkspace("Ошибка синтаксиса JavaScript: " + err.Error())
	}
	return nil
}
func (s *Server) savePlaceholderTag(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		Code    string `json:"code"`
		Name    string `json:"name"`
		TypeID  string `json:"type_id"`
		Script  string `json:"script"`
		Active  *bool  `json:"active"`
		Version int    `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if err := validatePlaceholder(in.Code, in.Name, in.Script); err != nil {
		workspaceFail(w, err)
		return
	}
	if in.TypeID != "" && !goodID(in.TypeID) {
		fail(w, 400, "Выберите тип документа")
		return
	}
	if in.TypeID != "" {
		var exists bool
		if s.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM document_types WHERE id=$1)", in.TypeID).Scan(&exists) != nil || !exists {
			fail(w, 400, "Тип документа не найден")
			return
		}
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	var id string
	var version int
	if r.PathValue("id") == "" {
		err := s.db.QueryRow(r.Context(), "INSERT INTO placeholder_tags(code,name,type_id,script,active,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,version", in.Code, in.Name, nullableStr(in.TypeID), in.Script, active, me(r).ID).Scan(&id, &version)
		if err != nil {
			errDB(w, err)
			return
		}
		respond(w, 201, map[string]any{"id": id, "version": version})
		return
	}
	if !goodID(r.PathValue("id")) || in.Version < 1 {
		fail(w, 400, "Укажите тег и его версию")
		return
	}
	err := s.db.QueryRow(r.Context(), "UPDATE placeholder_tags SET code=$1,name=$2,type_id=$3,script=$4,active=$5,version=version+1,updated_at=clock_timestamp() WHERE id=$6 AND version=$7 RETURNING id,version", in.Code, in.Name, nullableStr(in.TypeID), in.Script, active, r.PathValue("id"), in.Version).Scan(&id, &version)
	if err == pgx.ErrNoRows {
		fail(w, 409, "Тег изменился или удалён. Обновите список")
		return
	}
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]any{"id": id, "version": version})
}
func (s *Server) placeholderContext(ctx context.Context, id string, p Principal) (json.RawMessage, string, error) {
	if !s.readAllowed(ctx, id, p) {
		return nil, "", workspaceError{404, "Документ не найден"}
	}
	var raw []byte
	var typeID string
	query := `SELECT d.type_id,jsonb_build_object('id',d.id,'title',d.title,'summary',d.summary,'status',d.status,'registration_number',coalesce(d.registration_number,''),'project_number',coalesce(d.project_number,''),'created_at',d.created_at,'updated_at',d.updated_at,'due_at',d.due_at,'priority',d.priority,'tags',d.tags,'fields',d.fields,'type',jsonb_build_object('id',t.id,'code',t.code,'name',t.name),'author',jsonb_build_object('id',a.id,'full_name',a.full_name,'email',a.email),'assignee',CASE WHEN u.id IS NULL THEN NULL ELSE jsonb_build_object('id',u.id,'full_name',u.full_name,'email',u.email) END,'department',CASE WHEN dep.id IS NULL THEN NULL ELSE jsonb_build_object('id',dep.id,'name',dep.name) END) FROM documents d JOIN document_types t ON t.id=d.type_id JOIN users a ON a.id=d.author_id LEFT JOIN users u ON u.id=d.assignee_id LEFT JOIN departments dep ON dep.id=d.department_id WHERE d.id=$1 AND d.deleted_at IS NULL`
	if err := s.db.QueryRow(ctx, query, id).Scan(&typeID, &raw); err != nil {
		return nil, "", err
	}
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, "", err
	}
	fields, _ := card["fields"].(map[string]any)
	resolved := map[string]any{}
	var defs []byte
	if err := s.db.QueryRow(ctx, "SELECT fields FROM document_types WHERE id=$1", typeID).Scan(&defs); err != nil {
		return nil, "", err
	}
	var definition []FieldDef
	_ = json.Unmarshal(defs, &definition)
	resolve := func(value any) any {
		var uid string
		if text, ok := value.(string); ok {
			uid = text
		}
		if !goodID(uid) {
			return nil
		}
		var data []byte
		if s.db.QueryRow(ctx, `SELECT jsonb_build_object('id',u.id,'full_name',u.full_name,'email',u.email,'department_name',coalesce(d.name,''),'position_name',coalesce(p.name,'')) FROM users u LEFT JOIN departments d ON d.id=u.department_id LEFT JOIN positions p ON p.id=u.position_id WHERE u.id=$1`, uid).Scan(&data) != nil {
			return nil
		}
		var result any
		_ = json.Unmarshal(data, &result)
		return result
	}
	for _, f := range definition {
		if f.Type == "user" {
			resolved[f.Key] = resolve(fields[f.Key])
		}
		if f.Type == "users" {
			items := []any{}
			if ids, ok := fields[f.Key].([]any); ok {
				for _, uid := range ids {
					if person := resolve(uid); person != nil {
						items = append(items, person)
					}
				}
			}
			resolved[f.Key] = items
		}
	}
	card["resolved_fields"] = resolved
	raw, err := json.Marshal(card)
	return raw, typeID, err
}
func (s *Server) testPlaceholderTag(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		DocumentID string `json:"document_id"`
		Script     string `json:"script"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !goodID(in.DocumentID) {
		fail(w, 400, "Выберите документ для проверки")
		return
	}
	if err := validatePlaceholder("test", "Проверка", in.Script); err != nil {
		workspaceFail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	raw, _, err := s.placeholderContext(ctx, in.DocumentID, me(r))
	if err != nil {
		workspaceFail(w, err)
		return
	}
	value, err := evaluatePlaceholder(ctx, in.Script, raw)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	respond(w, 200, map[string]string{"value": value})
}
func (s *Server) placeholderValues(ctx context.Context, id string, p Principal, wanted []string) (map[string]string, map[string]int, error) {
	raw, typeID, err := s.placeholderContext(ctx, id, p)
	if err != nil {
		return nil, nil, err
	}
	tags, err := s.loadPlaceholderTags(ctx, typeID, false)
	if err != nil {
		return nil, nil, err
	}
	selected := map[string]placeholderTag{}
	for _, tag := range tags {
		selected[tag.Code] = tag
	}
	values := map[string]string{}
	versions := map[string]int{}
	for _, code := range wanted {
		if _, done := values[code]; done {
			continue
		}
		tag, ok := selected[code]
		if !ok {
			return nil, nil, badWorkspace("Для Tag «" + code + "» не настроен активный плейсхолдер этого типа документа")
		}
		value, err := evaluatePlaceholder(ctx, tag.Script, raw)
		if err != nil {
			return nil, nil, badWorkspace("Tag «" + code + "»: " + err.Error())
		}
		values[code] = value
		versions[tag.ID] = tag.Version
	}
	return values, versions, nil
}
func (s *Server) documentPlaceholderValues(w http.ResponseWriter, r *http.Request) {
	raw, typeID, err := s.placeholderContext(r.Context(), r.PathValue("id"), me(r))
	if err != nil {
		workspaceFail(w, err)
		return
	}
	tags, err := s.loadPlaceholderTags(r.Context(), typeID, false)
	if err != nil {
		errDB(w, err)
		return
	}
	selected := map[string]placeholderTag{}
	for _, tag := range tags {
		selected[tag.Code] = tag
	}
	if len(selected) > 100 {
		fail(w, 400, "Слишком много тегов для предпросмотра")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	values := []map[string]any{}
	for _, tag := range selected {
		value, err := evaluatePlaceholder(ctx, tag.Script, raw)
		item := map[string]any{"code": tag.Code, "name": tag.Name, "value": value}
		if err != nil {
			item["error"] = err.Error()
		}
		values = append(values, item)
	}
	respond(w, 200, values)
}
