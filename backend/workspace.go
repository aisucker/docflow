package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/export", s.authorize(s.exportDocumentRegister))
	mux.HandleFunc("GET /api/v1/workspace/analytics", s.authorize(s.workspaceAnalytics))
	mux.HandleFunc("GET /api/v1/workspace/calendar", s.authorize(s.workspaceCalendar))
	mux.HandleFunc("GET /api/v1/workspace/views", s.authorize(s.savedViews))
	mux.HandleFunc("POST /api/v1/workspace/views", s.authorize(s.saveView))
	mux.HandleFunc("DELETE /api/v1/workspace/views/{id}", s.authorize(s.deleteView))
	mux.HandleFunc("POST /api/v1/workspace/bulk", s.authorize(s.bulkDocuments))
	mux.HandleFunc("GET /api/v1/documents/{id}/workspace", s.authorize(s.documentWorkspace))
	mux.HandleFunc("PUT /api/v1/documents/{id}/favorite", s.authorize(s.favoriteDocument))
	mux.HandleFunc("PATCH /api/v1/documents/{id}/metadata", s.authorize(s.documentMetadata))
	mux.HandleFunc("POST /api/v1/documents/{id}/comments", s.authorize(s.commentDocument))
	mux.HandleFunc("POST /api/v1/documents/{id}/checklist", s.authorize(s.addChecklist))
	mux.HandleFunc("PATCH /api/v1/documents/{id}/checklist/{item}", s.authorize(s.updateChecklist))
	mux.HandleFunc("DELETE /api/v1/documents/{id}/checklist/{item}", s.authorize(s.removeChecklist))
	mux.HandleFunc("POST /api/v1/documents/{id}/links", s.authorize(s.linkDocument))
	mux.HandleFunc("DELETE /api/v1/documents/{id}/links/{item}", s.authorize(s.unlinkDocument))
	mux.HandleFunc("POST /api/v1/documents/{id}/duplicate", s.authorize(s.duplicateDocument))
	mux.HandleFunc("GET /api/v1/documents/{id}/versions", s.authorize(s.documentVersions))
	mux.HandleFunc("GET /api/v1/documents/{id}/versions/compare", s.authorize(s.compareDocumentVersions))
}

type workspaceDocument struct {
	author, assignee string
	updated          time.Time
}

func lockWorkspaceDocument(ctx context.Context, tx pgx.Tx, p Principal, id string, write bool) (workspaceDocument, error) {
	var d workspaceDocument
	if !goodID(id) {
		return d, badWorkspace("Некорректный документ")
	}
	err := tx.QueryRow(ctx, `SELECT d.author_id,coalesce(d.assignee_id::text,''),d.updated_at FROM documents d WHERE d.id=$3 AND `+workspaceACL+` FOR UPDATE`, p.ID, p.Role, id).Scan(&d.author, &d.assignee, &d.updated)
	if err == pgx.ErrNoRows {
		return d, workspaceError{404, "Документ не найден"}
	}
	if err != nil {
		return d, err
	}
	if write && p.Role != "admin" && p.ID != d.author && p.ID != d.assignee {
		return d, workspaceError{403, "Изменять реквизиты может автор или ответственный"}
	}
	return d, nil
}
func (s *Server) workspaceWrite(w http.ResponseWriter, r *http.Request, write bool, action func(pgx.Tx, workspaceDocument) error) {
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	doc, err := lockWorkspaceDocument(r.Context(), tx, me(r), r.PathValue("id"), write)
	if err == nil {
		_, err = tx.Exec(r.Context(), "SELECT set_config('docjet.actor_id',$1,true)", me(r).ID)
	}
	if err == nil {
		err = action(tx, doc)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		workspaceFail(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func normalizeTags(tags []string) ([]string, error) {
	if len(tags) > 40 {
		return nil, badWorkspace("Слишком много меток")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || len([]rune(tag)) > 40 {
			return nil, badWorkspace("Метка должна содержать 1–40 символов")
		}
		for _, c := range tag {
			if unicode.IsControl(c) {
				return nil, badWorkspace("Недопустимый символ в метке")
			}
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	if len(out) > 20 {
		return nil, badWorkspace("Допускается до 20 меток")
	}
	sort.Strings(out)
	return out, nil
}
func (s *Server) favoriteDocument(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Favorite *bool `json:"favorite"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Favorite == nil {
		fail(w, 400, "Укажите favorite")
		return
	}
	s.workspaceWrite(w, r, false, func(tx pgx.Tx, _ workspaceDocument) error {
		return setFavorite(r.Context(), tx, me(r).ID, r.PathValue("id"), *in.Favorite)
	})
}
func setFavorite(ctx context.Context, tx pgx.Tx, user, doc string, value bool) error {
	var err error
	if value {
		_, err = tx.Exec(ctx, "INSERT INTO document_favorites(user_id,document_id) VALUES($1,$2) ON CONFLICT DO NOTHING", user, doc)
	} else {
		_, err = tx.Exec(ctx, "DELETE FROM document_favorites WHERE user_id=$1 AND document_id=$2", user, doc)
	}
	return err
}
func (s *Server) documentMetadata(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Priority *string   `json:"priority"`
		Tags     *[]string `json:"tags"`
		Expected string    `json:"expected_updated_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Priority == nil && in.Tags == nil {
		fail(w, 400, "Укажите метки или приоритет")
		return
	}
	if in.Priority != nil && !validPriority(*in.Priority) {
		fail(w, 400, "Неизвестный приоритет")
		return
	}
	var tags []string
	var err error
	if in.Tags != nil {
		tags, err = normalizeTags(*in.Tags)
		if err != nil {
			workspaceFail(w, err)
			return
		}
	}
	expected, err := time.Parse(time.RFC3339Nano, in.Expected)
	if err != nil {
		fail(w, 400, "Обновите карточку: нужен expected_updated_at")
		return
	}
	s.workspaceWrite(w, r, true, func(tx pgx.Tx, d workspaceDocument) error {
		if !d.updated.Equal(expected) {
			return workspaceError{409, "Документ изменился. Обновите карточку"}
		}
		if _, err := tx.Exec(r.Context(), "UPDATE documents SET priority=coalesce($1,priority),tags=coalesce($2,tags),updated_at=clock_timestamp() WHERE id=$3", in.Priority, tags, r.PathValue("id")); err != nil {
			return err
		}
		return event(r.Context(), tx, r.PathValue("id"), me(r).ID, "document.metadata_updated", map[string]any{"priority": in.Priority, "tags": tags})
	})
}
func (s *Server) documentWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	relatedACL := strings.ReplaceAll(workspaceACL, "d.", "linked.")
	query := `SELECT jsonb_build_object('can_edit',coalesce(($2='admin' OR d.author_id=$1 OR d.assignee_id=$1),false),'priority',d.priority,'tags',d.tags,'updated_at',d.updated_at,'favorite',EXISTS(SELECT 1 FROM document_favorites f WHERE f.document_id=d.id AND f.user_id=$1),
 'comments',(SELECT coalesce(jsonb_agg(row),'[]') FROM (SELECT c.id,c.body,c.mention_ids,c.created_at,u.full_name author_name FROM document_comments c JOIN users u ON u.id=c.author_id WHERE c.document_id=d.id ORDER BY c.created_at DESC,c.id LIMIT 100) row),
 'checklist',(SELECT coalesce(jsonb_agg(row),'[]') FROM (SELECT c.id,c.label,c.done,u.full_name completed_by FROM document_checklist c LEFT JOIN users u ON u.id=c.completed_by WHERE c.document_id=d.id ORDER BY c.created_at,c.id LIMIT 200) row),
 'links',(SELECT coalesce(jsonb_agg(row),'[]') FROM (SELECT l.id,l.kind,linked.id document_id,linked.title,linked.status,(l.source_id=d.id) outgoing FROM document_links l JOIN documents linked ON linked.id=CASE WHEN l.source_id=d.id THEN l.target_id ELSE l.source_id END WHERE (l.source_id=d.id OR l.target_id=d.id) AND ` + relatedACL + ` ORDER BY l.created_at DESC LIMIT 100) row)) FROM documents d WHERE d.id=$3 AND ` + workspaceACL
	var raw []byte
	if err := s.db.QueryRow(r.Context(), query, me(r).ID, me(r).Role, id).Scan(&raw); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, json.RawMessage(raw))
}
func (s *Server) commentDocument(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body     string   `json:"body"`
		Mentions []string `json:"mentions"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" || len([]rune(in.Body)) > 4000 || len(in.Mentions) > 20 {
		fail(w, 400, "Комментарий: 1–4000 символов, до 20 упоминаний")
		return
	}
	s.workspaceWrite(w, r, false, func(tx pgx.Tx, _ workspaceDocument) error {
		mentions := []string{}
		seen := map[string]bool{}
		for _, user := range in.Mentions {
			if !goodID(user) {
				return badWorkspace("Некорректный участник")
			}
			if seen[user] {
				continue
			}
			seen[user] = true
			var role string
			if err := tx.QueryRow(r.Context(), "SELECT role FROM users WHERE id=$1 AND active", user).Scan(&role); err != nil {
				return badWorkspace("Участник отключён или не найден")
			}
			var allowed bool
			if err := tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM documents d WHERE d.id=$3 AND "+workspaceACL+")", user, role, r.PathValue("id")).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return badWorkspace("Упомянуть можно только сотрудника с доступом к документу")
			}
			mentions = append(mentions, user)
		}
		var id string
		if err := tx.QueryRow(r.Context(), "INSERT INTO document_comments(document_id,author_id,body,mention_ids) VALUES($1,$2,$3,$4::uuid[]) RETURNING id", r.PathValue("id"), me(r).ID, in.Body, mentions).Scan(&id); err != nil {
			return err
		}
		var title string
		if err := tx.QueryRow(r.Context(), "SELECT title FROM documents WHERE id=$1", r.PathValue("id")).Scan(&title); err != nil {
			return err
		}
		for _, user := range mentions {
			if user != me(r).ID {
				if err := insertNotification(r.Context(), tx, user, r.PathValue("id"), "", "mention", "Вас упомянули в обсуждении", title, "mention:"+id); err != nil {
					return err
				}
			}
		}
		return event(r.Context(), tx, r.PathValue("id"), me(r).ID, "document.comment_added", map[string]any{"comment_id": id, "mentions": mentions})
	})
}
func (s *Server) addChecklist(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label string `json:"label"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" || len([]rune(in.Label)) > 300 {
		fail(w, 400, "Пункт: 1–300 символов")
		return
	}
	s.workspaceWrite(w, r, true, func(tx pgx.Tx, _ workspaceDocument) error {
		var count int
		if err := tx.QueryRow(r.Context(), "SELECT count(*) FROM document_checklist WHERE document_id=$1", r.PathValue("id")).Scan(&count); err != nil {
			return err
		}
		if count >= 200 {
			return badWorkspace("Не больше 200 пунктов")
		}
		_, err := tx.Exec(r.Context(), "INSERT INTO document_checklist(document_id,label,created_by) VALUES($1,$2,$3)", r.PathValue("id"), in.Label, me(r).ID)
		if err != nil {
			return err
		}
		return event(r.Context(), tx, r.PathValue("id"), me(r).ID, "document.checklist_added", map[string]any{"label": in.Label})
	})
}
func (s *Server) updateChecklist(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Done *bool `json:"done"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Done == nil || !goodID(r.PathValue("item")) {
		fail(w, 400, "Укажите пункт и done")
		return
	}
	s.workspaceWrite(w, r, true, func(tx pgx.Tx, _ workspaceDocument) error {
		tag, err := tx.Exec(r.Context(), "UPDATE document_checklist SET done=$1,completed_by=CASE WHEN $1 THEN $2::uuid ELSE NULL END,completed_at=CASE WHEN $1 THEN now() ELSE NULL END WHERE id=$3 AND document_id=$4", *in.Done, me(r).ID, r.PathValue("item"), r.PathValue("id"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return workspaceError{404, "Пункт не найден"}
		}
		return event(r.Context(), tx, r.PathValue("id"), me(r).ID, "document.checklist_changed", map[string]any{"item": r.PathValue("item"), "done": *in.Done})
	})
}
func (s *Server) removeChecklist(w http.ResponseWriter, r *http.Request) {
	s.workspaceWrite(w, r, true, func(tx pgx.Tx, _ workspaceDocument) error {
		if !goodID(r.PathValue("item")) {
			return badWorkspace("Некорректный пункт")
		}
		tag, err := tx.Exec(r.Context(), "DELETE FROM document_checklist WHERE id=$1 AND document_id=$2", r.PathValue("item"), r.PathValue("id"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return workspaceError{404, "Пункт не найден"}
		}
		return event(r.Context(), tx, r.PathValue("id"), me(r).ID, "document.checklist_removed", map[string]any{"item": r.PathValue("item")})
	})
}
func (s *Server) linkDocument(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TargetID string `json:"target_id"`
		Kind     string `json:"kind"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !goodID(in.TargetID) || in.TargetID == r.PathValue("id") || (in.Kind != "related" && in.Kind != "basis" && in.Kind != "response_to") {
		fail(w, 400, "Выберите другой документ и вид связи")
		return
	}
	s.workspaceWrite(w, r, true, func(tx pgx.Tx, _ workspaceDocument) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM documents d WHERE d.id=$3 AND "+workspaceACL+")", me(r).ID, me(r).Role, in.TargetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return workspaceError{404, "Связанный документ недоступен"}
		}
		_, err := tx.Exec(r.Context(), "INSERT INTO document_links(source_id,target_id,kind,created_by) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", r.PathValue("id"), in.TargetID, in.Kind, me(r).ID)
		if err != nil {
			return err
		}
		return event(r.Context(), tx, r.PathValue("id"), me(r).ID, "document.link_added", map[string]any{"target_id": in.TargetID, "kind": in.Kind})
	})
}
func (s *Server) unlinkDocument(w http.ResponseWriter, r *http.Request) {
	s.workspaceWrite(w, r, true, func(tx pgx.Tx, _ workspaceDocument) error {
		if !goodID(r.PathValue("item")) {
			return badWorkspace("Некорректная связь")
		}
		tag, err := tx.Exec(r.Context(), "DELETE FROM document_links WHERE id=$1 AND source_id=$2", r.PathValue("item"), r.PathValue("id"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return workspaceError{404, "Связь не найдена"}
		}
		return event(r.Context(), tx, r.PathValue("id"), me(r).ID, "document.link_removed", map[string]any{"link": r.PathValue("item")})
	})
}

func (s *Server) savedViews(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), "SELECT id,name,filters FROM saved_document_views WHERE user_id=$1 ORDER BY name", me(r).ID)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name string
		var raw []byte
		if err = rows.Scan(&id, &name, &raw); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "filters": json.RawMessage(raw)})
	}
	if rows.Err() != nil {
		errDB(w, rows.Err())
		return
	}
	respond(w, 200, out)
}
func (s *Server) saveView(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string            `json:"name"`
		Filters map[string]string `json:"filters"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 80 || len(in.Filters) > 16 {
		fail(w, 400, "Укажите название до 80 символов")
		return
	}
	allowed := map[string]bool{"q": true, "status": true, "type_id": true, "author_id": true, "assignee_id": true, "department_id": true, "favorite": true, "priority": true, "tag": true, "due": true, "created_from": true, "created_to": true, "sort": true, "journal": true}
	q := url.Values{}
	for key, value := range in.Filters {
		if !allowed[key] || len(value) > 1000 {
			fail(w, 400, "Неизвестная настройка представления")
			return
		}
		q.Set(key, value)
	}
	if _, err := buildDocumentFilter(me(r), q, 100); err != nil {
		workspaceFail(w, err)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	if err = tx.QueryRow(r.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", me(r).ID).Scan(&id); err != nil {
		errDB(w, err)
		return
	}
	var n int
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM saved_document_views WHERE user_id=$1", me(r).ID).Scan(&n); err != nil {
		errDB(w, err)
		return
	}
	if n >= 20 {
		fail(w, 400, "Не больше 20 личных представлений")
		return
	}
	if in.Filters == nil {
		in.Filters = map[string]string{}
	}
	raw, _ := json.Marshal(in.Filters)
	if err = tx.QueryRow(r.Context(), "INSERT INTO saved_document_views(user_id,name,filters) VALUES($1,$2,$3) RETURNING id", me(r).ID, in.Name, raw).Scan(&id); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) deleteView(w http.ResponseWriter, r *http.Request) {
	if !goodID(r.PathValue("id")) {
		fail(w, 400, "Некорректное представление")
		return
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM saved_document_views WHERE id=$1 AND user_id=$2", r.PathValue("id"), me(r).ID)
	if err != nil {
		errDB(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "Представление не найдено")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) bulkDocuments(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs      []string `json:"ids"`
		Action   string   `json:"action"`
		Priority string   `json:"priority"`
		Tag      string   `json:"tag"`
		Favorite bool     `json:"favorite"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > 50 {
		fail(w, 400, "Выберите 1–50 документов")
		return
	}
	if in.Action != "priority" && in.Action != "tag_add" && in.Action != "favorite" {
		fail(w, 400, "Неизвестное массовое действие")
		return
	}
	if in.Action == "priority" && !validPriority(in.Priority) {
		fail(w, 400, "Некорректный приоритет")
		return
	}
	if in.Action == "tag_add" {
		tags, err := normalizeTags([]string{in.Tag})
		if err != nil {
			workspaceFail(w, err)
			return
		}
		in.Tag = tags[0]
	}
	for _, id := range in.IDs {
		if !goodID(id) {
			fail(w, 400, "Некорректный документ")
			return
		}
	}
	results := []map[string]any{}
	seen := map[string]bool{}
	sort.Strings(in.IDs)
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		err := s.bulkDocument(r.Context(), me(r), id, in.Action, in.Priority, in.Tag, in.Favorite)
		result := map[string]any{"id": id, "ok": err == nil}
		if err != nil {
			if e, ok := err.(workspaceError); ok {
				result["error"] = e.message
			} else {
				result["error"] = "Не удалось применить действие"
			}
		}
		results = append(results, result)
	}
	respond(w, 200, map[string]any{"results": results})
}
func (s *Server) bulkDocument(ctx context.Context, p Principal, id, action, priority, tag string, favorite bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockWorkspaceDocument(ctx, tx, p, id, action != "favorite"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('docjet.actor_id',$1,true)", p.ID); err != nil {
		return err
	}
	switch action {
	case "favorite":
		err = setFavorite(ctx, tx, p.ID, id, favorite)
	case "priority":
		_, err = tx.Exec(ctx, "UPDATE documents SET priority=$1,updated_at=clock_timestamp() WHERE id=$2", priority, id)
	case "tag_add":
		var tags []string
		if err = tx.QueryRow(ctx, "SELECT tags FROM documents WHERE id=$1", id).Scan(&tags); err == nil {
			tags, err = normalizeTags(append(tags, tag))
			if err == nil {
				_, err = tx.Exec(ctx, "UPDATE documents SET tags=$1,updated_at=clock_timestamp() WHERE id=$2", tags, id)
			}
		}
	}
	if err != nil {
		return err
	}
	if action != "favorite" {
		if err = event(ctx, tx, id, p.ID, "document.bulk_updated", map[string]any{"action": action, "priority": priority, "tag": tag}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Server) duplicateDocument(w http.ResponseWriter, r *http.Request) {
	if !s.readAllowed(r.Context(), r.PathValue("id"), me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	var input struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &input) {
		return
	}
	var v documentInput
	var raw []byte
	var priority string
	var tags []string
	if err := s.db.QueryRow(r.Context(), "SELECT type_id,title,summary,coalesce(assignee_id::text,''),coalesce(department_id::text,''),due_at,fields,priority,tags FROM documents WHERE id=$1 AND deleted_at IS NULL", r.PathValue("id")).Scan(&v.TypeID, &v.Title, &v.Summary, &v.AssigneeID, &v.DepartmentID, &v.DueAt, &raw, &priority, &tags); err != nil {
		errDB(w, err)
		return
	}
	if json.Unmarshal(raw, &v.Fields) != nil {
		fail(w, 500, "Повреждены поля документа")
		return
	}
	if strings.TrimSpace(input.Title) != "" {
		v.Title = strings.TrimSpace(input.Title)
	} else {
		v.Title = "Копия: " + v.Title
	}
	if len([]rune(v.Title)) > 500 {
		fail(w, 400, "Название копии длиннее 500 символов")
		return
	}
	if msg := s.checkDocFields(r.Context(), v.TypeID, v.Fields); msg != "" {
		fail(w, 400, msg)
		return
	}
	if msg := s.checkCardValues(r.Context(), v); msg != "" {
		fail(w, 400, msg)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO documents(type_id,title,summary,author_id,assignee_id,department_id,due_at,fields,priority,tags,workflow_key) SELECT id,$2,$3,$4,$5,$6,$7,$8,$9,$10,default_workflow_key FROM document_types WHERE id=$1 AND active RETURNING id`, v.TypeID, v.Title, v.Summary, me(r).ID, nullableStr(v.AssigneeID), nullableStr(v.DepartmentID), nullableTime(v.DueAt), raw, priority, tags).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	if err = event(r.Context(), tx, id, me(r).ID, "document.duplicated", map[string]any{"source_id": r.PathValue("id")}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func (s *Server) documentVersions(w http.ResponseWriter, r *http.Request) {
	if !s.readAllowed(r.Context(), r.PathValue("id"), me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	rows, err := s.db.Query(r.Context(), "SELECT v.version,v.created_at,coalesce(u.full_name,'Система') FROM document_revisions v LEFT JOIN users u ON u.id=v.editor_id WHERE v.document_id=$1 ORDER BY v.version DESC LIMIT 100", r.PathValue("id"))
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var version int
		var stamp time.Time
		var name string
		if err = rows.Scan(&version, &stamp, &name); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, map[string]any{"version": version, "created_at": stamp, "editor": name})
	}
	if rows.Err() != nil {
		errDB(w, rows.Err())
		return
	}
	respond(w, 200, out)
}
func (s *Server) compareDocumentVersions(w http.ResponseWriter, r *http.Request) {
	if !s.readAllowed(r.Context(), r.PathValue("id"), me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	from, e1 := strconv.Atoi(r.URL.Query().Get("from"))
	to, e2 := strconv.Atoi(r.URL.Query().Get("to"))
	if e1 != nil || e2 != nil || from < 1 || to < 1 {
		fail(w, 400, "Выберите две версии")
		return
	}
	snapshots := []map[string]any{}
	for _, version := range []int{from, to} {
		var raw []byte
		if err := s.db.QueryRow(r.Context(), "SELECT snapshot FROM document_revisions WHERE document_id=$1 AND version=$2", r.PathValue("id"), version).Scan(&raw); err != nil {
			errDB(w, err)
			return
		}
		var card map[string]any
		if json.Unmarshal(raw, &card) != nil {
			fail(w, 500, "Повреждена версия")
			return
		}
		if fields, ok := card["fields"].(map[string]any); ok {
			for k, v := range fields {
				card["fields."+k] = v
			}
		}
		delete(card, "fields")
		snapshots = append(snapshots, card)
	}
	keys := map[string]bool{}
	for _, card := range snapshots {
		for key := range card {
			keys[key] = true
		}
	}
	ordered := []string{}
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	changes := []map[string]any{}
	for _, key := range ordered {
		if !reflect.DeepEqual(snapshots[0][key], snapshots[1][key]) {
			changes = append(changes, map[string]any{"field": key, "before": snapshots[0][key], "after": snapshots[1][key]})
		}
	}
	respond(w, 200, map[string]any{"from": from, "to": to, "changes": changes})
}
