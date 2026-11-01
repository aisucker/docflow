package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const workspaceACL = `d.deleted_at IS NULL AND ($2 IN ('admin','registrar') OR d.author_id=$1 OR d.assignee_id=$1 OR EXISTS(SELECT 1 FROM workflow_tasks acl_task WHERE acl_task.document_id=d.id AND acl_task.assignee_id=$1))`

type workspaceError struct {
	status  int
	message string
}

func (e workspaceError) Error() string  { return e.message }
func badWorkspace(message string) error { return workspaceError{400, message} }
func workspaceFail(w http.ResponseWriter, err error) {
	if e, ok := err.(workspaceError); ok {
		fail(w, e.status, e.message)
	} else {
		errDB(w, err)
	}
}

type documentFilter struct {
	where, order  string
	args          []any
	limit, offset int
}

func buildDocumentFilter(p Principal, q url.Values, maxLimit int) (documentFilter, error) {
	f := documentFilter{where: workspaceACL, args: []any{p.ID, p.Role}, order: "d.created_at DESC,d.id", limit: 50}
	add := func(clause string, value any) {
		f.args = append(f.args, value)
		f.where += " AND " + fmt.Sprintf(clause, len(f.args))
	}
	for key, clause := range map[string]string{"type_id": "d.type_id=$%d::uuid", "author_id": "d.author_id=$%d::uuid", "assignee_id": "d.assignee_id=$%d::uuid", "department_id": "d.department_id=$%d::uuid"} {
		if value := q.Get(key); value != "" {
			if !goodID(value) {
				return f, badWorkspace("Некорректный идентификатор фильтра " + key)
			}
			add(clause, value)
		}
	}
	if value := q.Get("q"); value != "" {
		if len([]rune(value)) > 500 {
			return f, badWorkspace("Поисковый запрос слишком длинный")
		}
		f.args = append(f.args, value)
		n := len(f.args)
		f.where += fmt.Sprintf(" AND (d.title ILIKE '%%'||$%d||'%%' OR d.summary ILIKE '%%'||$%d||'%%' OR d.registration_number ILIKE '%%'||$%d||'%%' OR d.project_number ILIKE '%%'||$%d||'%%')", n, n, n, n)
	}
	if value := q.Get("status"); value != "" {
		valid := false
		for _, s := range []string{"draft", "in_progress", "in_review", "approved", "completed", "returned", "rejected"} {
			if value == s {
				valid = true
			}
		}
		if !valid {
			return f, badWorkspace("Неизвестный статус")
		}
		add("d.status=$%d", value)
	}
	switch q.Get("journal") {
	case "mine":
		f.where += " AND d.author_id=$1"
	case "inbox":
		f.where += " AND EXISTS(SELECT 1 FROM workflow_tasks inbox_task WHERE inbox_task.document_id=d.id AND inbox_task.assignee_id=$1 AND inbox_task.status IN ('pending','in_progress'))"
	case "", "all":
	default:
		return f, badWorkspace("Неизвестный журнал")
	}
	if value := q.Get("favorite"); value == "true" {
		f.where += " AND EXISTS(SELECT 1 FROM document_favorites fav WHERE fav.document_id=d.id AND fav.user_id=$1)"
	}
	if value := q.Get("priority"); value != "" {
		if !validPriority(value) {
			return f, badWorkspace("Неизвестный приоритет")
		}
		add("d.priority=$%d", value)
	}
	if tag := strings.ToLower(strings.TrimSpace(q.Get("tag"))); tag != "" {
		add("$%d=ANY(d.tags)", tag)
	}
	for _, key := range []string{"created_from", "created_to", "due_from", "due_to"} {
		if value := q.Get(key); value != "" {
			stamp, err := time.Parse("2006-01-02", value)
			if err != nil {
				return f, badWorkspace("Неверная дата фильтра " + key)
			}
			stamp = time.Date(stamp.Year(), stamp.Month(), stamp.Day(), 0, 0, 0, 0, time.FixedZone("Qyzylorda", 5*3600))
			column := "d.created_at"
			if strings.HasPrefix(key, "due_") {
				column = "d.due_at"
			}
			operator := ">="
			if strings.HasSuffix(key, "_to") {
				stamp = stamp.AddDate(0, 0, 1)
				operator = "<"
			}
			add(column+operator+"$%d", stamp)
		}
	}
	switch q.Get("due") {
	case "overdue":
		f.where += " AND d.due_at<now() AND d.status NOT IN ('approved','completed','rejected')"
	case "today":
		f.where += " AND (d.due_at AT TIME ZONE 'Asia/Qyzylorda')::date=(now() AT TIME ZONE 'Asia/Qyzylorda')::date"
	case "week":
		f.where += " AND d.due_at>=now() AND d.due_at<now()+interval '7 days'"
	case "none":
		f.where += " AND d.due_at IS NULL"
	case "":
	default:
		return f, badWorkspace("Неизвестный фильтр срока")
	}
	orders := map[string]string{"newest": "d.created_at DESC,d.id", "oldest": "d.created_at,d.id", "due": "d.due_at ASC NULLS LAST,d.id", "updated": "d.updated_at DESC,d.id", "priority": "CASE d.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,d.created_at DESC,d.id"}
	if value := q.Get("sort"); value != "" {
		order, ok := orders[value]
		if !ok {
			return f, badWorkspace("Неизвестная сортировка")
		}
		f.order = order
	}
	for key, target := range map[string]*int{"limit": &f.limit, "offset": &f.offset} {
		if value := q.Get(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || (key == "limit" && (n < 1 || n > maxLimit)) || (key == "offset" && n > 100000) {
				return f, badWorkspace("Некорректный " + key)
			}
			*target = n
		}
	}
	return f, nil
}
func validPriority(value string) bool {
	return value == "low" || value == "normal" || value == "high" || value == "urgent"
}

func (s *Server) queryDocuments(ctx context.Context, p Principal, q url.Values, maxLimit int) ([]map[string]any, error) {
	f, err := buildDocumentFilter(p, q, maxLimit)
	if err != nil {
		return nil, err
	}
	f.args = append(f.args, f.limit, f.offset)
	query := `SELECT jsonb_build_object('id',d.id,'type_id',d.type_id,'type_name',dt.name,'title',d.title,'summary',d.summary,'status',d.status,'registration_number',coalesce(d.registration_number,''),'project_number',coalesce(d.project_number,''),'author_id',d.author_id,'author_name',u.full_name,'assignee_id',d.assignee_id,'department_id',d.department_id,'created_at',d.created_at,'updated_at',d.updated_at,'due_at',d.due_at,'priority',d.priority,'tags',d.tags,'favorite',EXISTS(SELECT 1 FROM document_favorites fav WHERE fav.document_id=d.id AND fav.user_id=$1)) FROM documents d JOIN document_types dt ON dt.id=d.type_id JOIN users u ON u.id=d.author_id WHERE ` + f.where + ` ORDER BY ` + f.order + fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(f.args)-1, len(f.args))
	rows, err := s.db.Query(ctx, query, f.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item map[string]any
		if err = json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Server) exportDocumentRegister(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	q.Set("limit", "5000")
	q.Set("offset", "0")
	items, err := s.queryDocuments(r.Context(), me(r), q, 5000)
	if err != nil {
		workspaceFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="docjet-register.csv"`)
	w.Header().Set("X-DocJet-Export-Limit", "5000")
	_, _ = w.Write([]byte("\xef\xbb\xbf"))
	writer := csv.NewWriter(w)
	writer.Comma = ';'
	_ = writer.Write([]string{"Название", "Тип", "Номер", "Статус", "Автор", "Приоритет", "Метки", "Срок"})
	for _, item := range items {
		row := []string{}
		for _, key := range []string{"title", "type_name", "registration_number", "status", "author_name", "priority", "tags", "due_at"} {
			value := ""
			if item[key] != nil {
				value = fmt.Sprint(item[key])
			}
			if key == "registration_number" && value == "" {
				value = fmt.Sprint(item["project_number"])
			}
			row = append(row, safeCSV(value))
		}
		if writer.Write(row) != nil {
			return
		}
	}
	writer.Flush()
}
func safeCSV(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if strings.ContainsAny(value[:min(len(value), 1)], "\t\r\n") || (trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
		return "'" + value
	}
	return value
}

func (s *Server) workspaceAnalytics(w http.ResponseWriter, r *http.Request) {
	f, err := buildDocumentFilter(me(r), r.URL.Query(), 100)
	if err != nil {
		workspaceFail(w, err)
		return
	}
	query := `WITH visible AS (SELECT d.* FROM documents d WHERE ` + f.where + `) SELECT jsonb_build_object('total',(SELECT count(*) FROM visible),'overdue',(SELECT count(*) FROM visible WHERE due_at<now() AND status NOT IN ('approved','completed','rejected')),'by_status',(SELECT coalesce(jsonb_object_agg(status,n),'{}') FROM (SELECT status,count(*) n FROM visible GROUP BY status) grouped),'by_priority',(SELECT coalesce(jsonb_object_agg(priority,n),'{}') FROM (SELECT priority,count(*) n FROM visible GROUP BY priority) grouped),'my_pending_tasks',(SELECT count(*) FROM workflow_tasks t JOIN visible d ON d.id=t.document_id WHERE t.assignee_id=$1 AND t.status IN ('pending','in_progress')),'my_overdue_tasks',(SELECT count(*) FROM workflow_tasks t JOIN visible d ON d.id=t.document_id WHERE t.assignee_id=$1 AND t.status IN ('pending','in_progress') AND d.due_at<now()))`
	var raw []byte
	if err = s.db.QueryRow(r.Context(), query, f.args...).Scan(&raw); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, json.RawMessage(raw))
}
func (s *Server) workspaceCalendar(w http.ResponseWriter, r *http.Request) {
	month, err := time.Parse("2006-01", r.URL.Query().Get("month"))
	if err != nil {
		fail(w, 400, "Месяц должен иметь формат YYYY-MM")
		return
	}
	q := r.URL.Query()
	q.Set("due_from", month.Format("2006-01-02"))
	q.Set("due_to", month.AddDate(0, 1, -1).Format("2006-01-02"))
	q.Set("limit", "501")
	q.Set("offset", "0")
	q.Set("sort", "due")
	items, err := s.queryDocuments(r.Context(), me(r), q, 501)
	if err != nil {
		workspaceFail(w, err)
		return
	}
	truncated := len(items) > 500
	if truncated {
		items = items[:500]
	}
	respond(w, 200, map[string]any{"items": items, "truncated": truncated})
}
