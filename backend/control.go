package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"

	"strings"
	"time"
)

func (s *Server) controlRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/desk", s.authorize(s.desk))
	m.HandleFunc("POST /api/v1/desk/items", s.authorize(s.saveDeskItem))
	m.HandleFunc("PUT /api/v1/desk/items/{id}", s.authorize(s.saveDeskItem))
	m.HandleFunc("DELETE /api/v1/desk/items/{id}", s.authorize(s.deleteDeskItem))
	m.HandleFunc("POST /api/v1/documents/{id}/seen", s.authorize(s.seenDocument))
	m.HandleFunc("GET /api/v1/bulletins", s.authorize(s.bulletins))
	m.HandleFunc("POST /api/v1/bulletins/{id}/ack", s.authorize(s.ackBulletin))
	m.HandleFunc("POST /api/v1/admin/bulletins", s.authorize(s.saveBulletin))
	m.HandleFunc("PUT /api/v1/admin/bulletins/{id}", s.authorize(s.saveBulletin))
	m.HandleFunc("GET /api/v1/admin/control", s.authorize(s.controlReport))
}
func (s *Server) jsonRows(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	rows, err := s.db.Query(r.Context(), query, args...)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, json.RawMessage(raw))
	}
	if rows.Err() != nil {
		errDB(w, rows.Err())
		return
	}
	respond(w, 200, out)
}
func (s *Server) desk(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("part") == "recent" {
		s.jsonRows(w, r, `SELECT jsonb_build_object('id',d.id,'title',d.title,'status',d.status,'seen_at',v.seen_at,'changed',d.updated_at>v.seen_at) FROM document_seen v JOIN documents d ON d.id=v.document_id WHERE v.user_id=$1 AND `+workspaceACL+` ORDER BY v.seen_at DESC LIMIT 100`, me(r).ID, me(r).Role)
		return
	}
	if r.URL.Query().Get("part") == "tasks" {
		s.jsonRows(w, r, `SELECT jsonb_build_object('id',t.id,'title',t.title,'document_id',d.id,'document_title',d.title,'due_at',d.due_at,'status',t.status) FROM workflow_tasks t JOIN documents d ON d.id=t.document_id WHERE t.assignee_id=$1 AND t.status IN ('pending','in_progress') AND `+workspaceACL+` ORDER BY d.due_at NULLS LAST,t.created_at LIMIT 200`, me(r).ID, me(r).Role)
		return
	}
	s.jsonRows(w, r, `SELECT jsonb_build_object('id',i.id,'kind',i.kind,'title',i.title,'body',i.body,'due_at',i.due_at,'done',i.done,'version',i.version,'collection',i.collection,'document_id',CASE WHEN `+workspaceACL+` THEN d.id END,'document_title',CASE WHEN `+workspaceACL+` THEN d.title END) FROM desk_items i LEFT JOIN documents d ON d.id=i.document_id WHERE i.user_id=$1 ORDER BY i.done,i.due_at NULLS LAST,i.updated_at DESC LIMIT 1000`, me(r).ID, me(r).Role)
}
func (s *Server) saveDeskItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind       string     `json:"kind"`
		Title      string     `json:"title"`
		Body       string     `json:"body"`
		Collection string     `json:"collection"`
		DocumentID string     `json:"document_id"`
		DueAt      *time.Time `json:"due_at"`
		Done       bool       `json:"done"`
		Version    int        `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if (in.Kind != "note" && in.Kind != "reminder" && in.Kind != "collection") || in.Title == "" || len([]rune(in.Title)) > 200 || len(in.Body) > 16000 || len([]rune(in.Collection)) > 100 {
		fail(w, 400, "Укажите название до 200 символов и корректный вид записи")
		return
	}
	if in.Kind == "reminder" && in.DueAt == nil {
		fail(w, 400, "Укажите срок напоминания")
		return
	}
	if in.DocumentID != "" && !s.readAllowed(r.Context(), in.DocumentID, me(r)) {
		fail(w, 404, "Документ недоступен")
		return
	}
	var id string
	var err error
	if r.PathValue("id") == "" {
		err = s.db.QueryRow(r.Context(), `INSERT INTO desk_items(user_id,kind,title,body,collection,document_id,due_at,done) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, me(r).ID, in.Kind, in.Title, in.Body, in.Collection, nullableStr(in.DocumentID), in.DueAt, in.Done).Scan(&id)
	} else {
		if !goodID(r.PathValue("id")) || in.Version < 1 {
			fail(w, 400, "Нужна версия записи")
			return
		}
		err = s.db.QueryRow(r.Context(), `UPDATE desk_items SET kind=$1,title=$2,body=$3,collection=$4,document_id=$5,due_at=$6,done=$7,version=version+1,updated_at=clock_timestamp() WHERE id=$8 AND user_id=$9 AND version=$10 RETURNING id`, in.Kind, in.Title, in.Body, in.Collection, nullableStr(in.DocumentID), in.DueAt, in.Done, r.PathValue("id"), me(r).ID, in.Version).Scan(&id)
	}
	if err != nil {
		fail(w, 409, "Запись недоступна или изменилась. Обновите список")
		return
	}
	respond(w, 200, map[string]string{"id": id})
}
func (s *Server) deleteDeskItem(w http.ResponseWriter, r *http.Request) {
	if !goodID(r.PathValue("id")) {
		fail(w, 400, "Некорректная запись")
		return
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM desk_items WHERE id=$1 AND user_id=$2", r.PathValue("id"), me(r).ID)
	if err != nil {
		errDB(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "Запись не найдена")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) seenDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ недоступен")
		return
	}
	_, err := s.db.Exec(r.Context(), "INSERT INTO document_seen(user_id,document_id) VALUES($1,$2) ON CONFLICT(user_id,document_id) DO UPDATE SET seen_at=clock_timestamp()", me(r).ID, id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

const visibleBulletin = `b.active AND b.starts_at<=now() AND (b.ends_at IS NULL OR b.ends_at>now()) AND (b.audience='all' OR b.audience=$2)`

func (s *Server) bulletins(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	if all && !mustRole(w, me(r), "admin") {
		return
	}
	s.jsonRows(w, r, `SELECT jsonb_build_object('id',b.id,'title',b.title,'body',b.body,'severity',b.severity,'audience',b.audience,'pinned',b.pinned,'active',b.active,'starts_at',b.starts_at,'ends_at',b.ends_at,'version',b.version,'acknowledged',EXISTS(SELECT 1 FROM bulletin_ack a WHERE a.bulletin_id=b.id AND a.user_id=$1),'read_count',(SELECT count(*) FROM bulletin_ack a WHERE a.bulletin_id=b.id)) FROM bulletins b WHERE $3::boolean OR (`+visibleBulletin+`) ORDER BY b.pinned DESC,b.starts_at DESC LIMIT 200`, me(r).ID, me(r).Role, all)
}
func (s *Server) ackBulletin(w http.ResponseWriter, r *http.Request) {
	if !goodID(r.PathValue("id")) {
		fail(w, 400, "Некорректное объявление")
		return
	}
	tag, err := s.db.Exec(r.Context(), `INSERT INTO bulletin_ack(user_id,bulletin_id) SELECT $1,b.id FROM bulletins b WHERE b.id=$3 AND `+visibleBulletin+` ON CONFLICT DO NOTHING`, me(r).ID, me(r).Role, r.PathValue("id"))
	if err != nil {
		errDB(w, err)
		return
	}
	_ = tag
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) saveBulletin(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		Title    string     `json:"title"`
		Body     string     `json:"body"`
		Severity string     `json:"severity"`
		Audience string     `json:"audience"`
		Pinned   bool       `json:"pinned"`
		Active   bool       `json:"active"`
		StartsAt time.Time  `json:"starts_at"`
		EndsAt   *time.Time `json:"ends_at"`
		Version  int        `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Title) == "" || len([]rune(in.Title)) > 200 || len(in.Body) > 16000 || (in.Severity != "info" && in.Severity != "important" && in.Severity != "urgent") || (in.Audience != "all" && in.Audience != "admin" && in.Audience != "employee" && in.Audience != "registrar") {
		fail(w, 400, "Проверьте название, важность и аудиторию")
		return
	}
	if in.StartsAt.IsZero() {
		in.StartsAt = time.Now()
	}
	if in.EndsAt != nil && !in.EndsAt.After(in.StartsAt) {
		fail(w, 400, "Окончание должно быть позже начала")
		return
	}
	var id string
	var err error
	if r.PathValue("id") == "" {
		err = s.db.QueryRow(r.Context(), `INSERT INTO bulletins(title,body,severity,audience,pinned,active,starts_at,ends_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, in.Title, in.Body, in.Severity, in.Audience, in.Pinned, in.Active, in.StartsAt, in.EndsAt, me(r).ID).Scan(&id)
	} else {
		if !goodID(r.PathValue("id")) || in.Version < 1 {
			fail(w, 400, "Нужна версия объявления")
			return
		}
		err = s.db.QueryRow(r.Context(), `UPDATE bulletins SET title=$1,body=$2,severity=$3,audience=$4,pinned=$5,active=$6,starts_at=$7,ends_at=$8,version=version+1 WHERE id=$9 AND version=$10 RETURNING id`, in.Title, in.Body, in.Severity, in.Audience, in.Pinned, in.Active, in.StartsAt, in.EndsAt, r.PathValue("id"), in.Version).Scan(&id)
	}
	if err != nil {
		fail(w, 409, "Объявление изменилось. Обновите список")
		return
	}
	respond(w, 200, map[string]string{"id": id})
}

var controlReports = map[string]string{
	"workload":         `SELECT u.full_name AS "Сотрудник",count(t.id) AS "Открытые задачи",count(t.id) FILTER(WHERE d.due_at<now()) AS "Просроченные" FROM users u LEFT JOIN workflow_tasks t ON t.assignee_id=u.id AND t.status IN ('pending','in_progress') AND EXISTS(SELECT 1 FROM documents live WHERE live.id=t.document_id AND live.deleted_at IS NULL) LEFT JOIN documents d ON d.id=t.document_id AND d.deleted_at IS NULL WHERE u.active GROUP BY u.id ORDER BY count(t.id) DESC`,
	"bottlenecks":      `SELECT t.title AS "Этап",count(*) AS "Открытые задачи",round(avg(extract(epoch FROM now()-t.created_at)/86400)::numeric,1) AS "Ожидание, дней" FROM workflow_tasks t JOIN documents d ON d.id=t.document_id WHERE t.status IN ('pending','in_progress') AND d.deleted_at IS NULL GROUP BY t.title ORDER BY count(*) DESC`,
	"overdue":          `SELECT d.title AS "Документ",u.full_name AS "Ответственный",d.due_at AS "Срок" FROM documents d LEFT JOIN users u ON u.id=d.assignee_id WHERE d.deleted_at IS NULL AND d.due_at<now() AND d.status NOT IN ('approved','completed','rejected') ORDER BY d.due_at`,
	"unassigned":       `SELECT title AS "Документ",status AS "Статус",created_at AS "Создан" FROM documents WHERE deleted_at IS NULL AND assignee_id IS NULL ORDER BY created_at DESC`,
	"inactive":         `SELECT d.title AS "Документ",u.full_name AS "Отключённый исполнитель",t.title AS "Задача" FROM workflow_tasks t JOIN users u ON u.id=t.assignee_id JOIN documents d ON d.id=t.document_id WHERE NOT u.active AND t.status IN ('pending','in_progress') AND d.deleted_at IS NULL`,
	"departments":      `SELECT d.name AS "Подразделение",count(u.id) AS "Активные сотрудники" FROM departments d LEFT JOIN users u ON u.department_id=d.id AND u.active GROUP BY d.id HAVING count(u.id)=0 ORDER BY d.name`,
	"types":            `SELECT name AS "Тип документа",code AS "Код" FROM document_types WHERE active AND coalesce(default_workflow_key,'')='' ORDER BY name`,
	"outbox":           `SELECT id AS "ID",event_type AS "Событие",attempts AS "Попытки",next_attempt_at AS "Следующая попытка" FROM outbox WHERE delivered_at IS NULL ORDER BY attempts DESC,id LIMIT 500`,
	"acknowledgements": `SELECT b.title AS "Объявление",u.full_name AS "Ознакомился",a.created_at AS "Дата" FROM bulletin_ack a JOIN bulletins b ON b.id=a.bulletin_id JOIN users u ON u.id=a.user_id ORDER BY a.created_at DESC`,
}

func (s *Server) controlReport(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	query, ok := controlReports[r.URL.Query().Get("report")]
	if !ok {
		fail(w, 400, "Выберите отчёт")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, "SELECT row_to_json(report) FROM ("+query+") report LIMIT 1000")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var raw []byte
		var v map[string]any
		if err = rows.Scan(&raw); err != nil {
			errDB(w, err)
			return
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		errDB(w, rows.Err())
		return
	}
	if r.URL.Query().Get("format") != "csv" {
		respond(w, 200, out)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="docjet-control.csv"`)
	w.Write([]byte("\xef\xbb\xbf"))
	writer := csv.NewWriter(w)
	writer.Comma = ';'
	fields := rows.FieldDescriptions()
	headers := []string{}
	_ = fields
	if len(out) > 0 {
		for key := range out[0] {
			headers = append(headers, key)
		}
	}
	sortStrings(headers)
	writer.Write(headers)
	for _, v := range out {
		line := []string{}
		for _, key := range headers {
			raw, _ := json.Marshal(v[key])
			value := strings.Trim(string(raw), `"`)
			line = append(line, safeCSV(value))
		}
		writer.Write(line)
	}
	writer.Flush()
}
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
