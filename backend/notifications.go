package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type notificationPreferences struct {
	Enabled           bool `json:"enabled"`
	TaskAssigned      bool `json:"task_assigned"`
	TaskDelegated     bool `json:"task_delegated"`
	TaskCompleted     bool `json:"task_completed"`
	DocumentReturned  bool `json:"document_returned"`
	DocumentRejected  bool `json:"document_rejected"`
	DocumentCompleted bool `json:"document_completed"`
	DueSoon           bool `json:"due_soon"`
	Overdue           bool `json:"overdue"`
	ReminderHours     int  `json:"reminder_hours"`
}

func defaultNotificationPreferences() notificationPreferences {
	return notificationPreferences{true, true, true, true, true, true, true, true, true, 24}
}
func notificationSettings(raw []byte) (notificationPreferences, error) {
	settings := defaultNotificationPreferences()
	err := json.Unmarshal(raw, &settings)
	return settings, err
}

func (s *Server) getNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	if err := s.db.QueryRow(r.Context(), "SELECT notification_preferences FROM users WHERE id=$1", me(r).ID).Scan(&raw); err != nil {
		errDB(w, err)
		return
	}
	settings, err := notificationSettings(raw)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, settings)
}
func (s *Server) saveNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	var patch map[string]json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	if len(patch) == 0 {
		fail(w, 400, "Укажите настройки уведомлений")
		return
	}
	allowed := map[string]bool{"enabled": true, "task_assigned": true, "task_delegated": true, "task_completed": true, "document_returned": true, "document_rejected": true, "document_completed": true, "due_soon": true, "overdue": true, "reminder_hours": true}
	for key, raw := range patch {
		if !allowed[key] || string(raw) == "null" {
			fail(w, 400, "Неизвестная или пустая настройка уведомлений")
			return
		}
		if key == "reminder_hours" {
			var hours int
			if json.Unmarshal(raw, &hours) != nil || hours < 1 || hours > 168 {
				fail(w, 400, "Напоминать можно за 1–168 часов")
				return
			}
		} else {
			var enabled bool
			if json.Unmarshal(raw, &enabled) != nil {
				fail(w, 400, "Настройки уведомлений должны быть true или false")
				return
			}
		}
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var raw []byte
	if err = tx.QueryRow(r.Context(), "SELECT notification_preferences FROM users WHERE id=$1 FOR UPDATE", me(r).ID).Scan(&raw); err != nil {
		errDB(w, err)
		return
	}
	settings, err := notificationSettings(raw)
	if err != nil {
		errDB(w, err)
		return
	}
	current, _ := json.Marshal(settings)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(current, &fields)
	for key, value := range patch {
		fields[key] = value
	}
	raw, _ = json.Marshal(fields)
	if _, err = tx.Exec(r.Context(), "UPDATE users SET notification_preferences=$1 WHERE id=$2", raw, me(r).ID); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	s.getNotificationPreferences(w, r)
}

// Settings are checked in the same transaction as the originating event.
// Deduplication makes retries and multiple worker instances safe.
func insertNotification(ctx context.Context, tx pgx.Tx, user, doc, task, kind, title, body, key string) error {
	_, err := tx.Exec(ctx, `INSERT INTO notifications(user_id,document_id,task_id,kind,title,body,dedupe_key)
 SELECT u.id,$2,$3,$4,$5,$6,$7 FROM users u WHERE u.id=$1 AND u.active=true
 AND coalesce((u.notification_preferences->>'enabled')::boolean,true)
 AND coalesce((u.notification_preferences->>$4)::boolean,true)
 ON CONFLICT(user_id,dedupe_key) DO NOTHING`, user, doc, nullableStr(task), kind, title, body, key)
	return err
}

func notifyEvent(ctx context.Context, tx pgx.Tx, eventID int64, docID, actor, kind string, raw []byte) error {
	switch kind {
	case "workflow.step_started", "workflow.task_delegated", "workflow.task_approved", "workflow.task_completed", "workflow.task_returned", "workflow.task_rejected", "workflow.approved", "workflow.completed":
	default:
		return nil
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		return err
	}
	var title, author, responsible string
	if err := tx.QueryRow(ctx, "SELECT title,author_id,coalesce(assignee_id::text,'') FROM documents WHERE id=$1", docID).Scan(&title, &author, &responsible); err != nil {
		return err
	}
	if kind == "workflow.step_started" {
		step, _ := detail["step"].(string)
		rows, err := tx.Query(ctx, `SELECT t.id,t.assignee_id,t.title FROM workflow_tasks t JOIN workflow_instances wi ON wi.id=t.instance_id WHERE t.document_id=$1 AND t.step_id=$2 AND wi.status='active' AND t.status IN ('pending','in_progress')`, docID, step)
		if err != nil {
			return err
		}
		type target struct{ id, user, title string }
		targets := []target{}
		for rows.Next() {
			var v target
			if err = rows.Scan(&v.id, &v.user, &v.title); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range targets {
			if err = insertNotification(ctx, tx, v.user, docID, v.id, "task_assigned", "Новая задача: "+v.title, title, "task_assigned:"+v.id); err != nil {
				return err
			}
		}
		return nil
	}
	if kind == "workflow.task_delegated" {
		user, _ := detail["assignee_id"].(string)
		task, _ := detail["delegated_task_id"].(string)
		return insertNotification(ctx, tx, user, docID, task, "task_delegated", "Вам поручена задача", title, "task_delegated:"+task)
	}
	notificationKind, heading := "task_completed", "Задача по документу выполнена"
	switch kind {
	case "workflow.task_approved":
		heading = "Получено согласование"
	case "workflow.task_returned":
		notificationKind, heading = "document_returned", "Документ возвращён на доработку"
	case "workflow.task_rejected":
		notificationKind, heading = "document_rejected", "Документ отклонён"
	case "workflow.approved":
		notificationKind, heading = "document_completed", "Документ согласован"
	case "workflow.completed":
		notificationKind, heading = "document_completed", "Документ исполнен"
	}
	seen := map[string]bool{}
	for _, user := range []string{author, responsible} {
		if user == "" || user == actor || seen[user] {
			continue
		}
		seen[user] = true
		if err := insertNotification(ctx, tx, user, docID, "", notificationKind, heading, title, fmt.Sprintf("event:%d", eventID)); err != nil {
			return err
		}
	}
	return nil
}

// Notification visibility follows current document access, including soft
// deletion. Reminder rows disappear when their task or deadline is obsolete.
const visibleNotifications = ` FROM notifications n JOIN documents d ON d.id=n.document_id
 WHERE n.user_id=$1 AND d.deleted_at IS NULL
 AND ($2 IN ('admin','registrar') OR d.author_id=$1 OR d.assignee_id=$1 OR EXISTS(SELECT 1 FROM workflow_tasks access_task WHERE access_task.document_id=d.id AND access_task.assignee_id=$1))
 AND (n.kind NOT IN ('due_soon','overdue') OR (d.due_at=n.deadline_at AND EXISTS(
 SELECT 1 FROM workflow_tasks reminder_task JOIN workflow_instances wi ON wi.id=reminder_task.instance_id
 WHERE reminder_task.id=n.task_id AND reminder_task.status IN ('pending','in_progress') AND wi.status='active')
 AND ((n.kind='due_soon' AND d.due_at>now()) OR (n.kind='overdue' AND d.due_at<=now()))))`

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			fail(w, 400, "limit должен быть от 1 до 100")
			return
		}
		limit = value
	}
	var before int64
	if raw := r.URL.Query().Get("before_id"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			fail(w, 400, "Некорректный before_id")
			return
		}
		before = value
	}
	unreadOnly := r.URL.Query().Get("unread_only") == "true"
	tx, err := s.db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var unread, maxID int64
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FILTER(WHERE n.read_at IS NULL),coalesce(max(n.id),0)"+visibleNotifications, me(r).ID, me(r).Role).Scan(&unread, &maxID); err != nil {
		errDB(w, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT n.id,n.document_id,coalesce(n.task_id::text,''),n.kind,n.title,n.body,n.created_at,n.read_at`+visibleNotifications+` AND ($3::bigint=0 OR n.id<$3) AND (NOT $4::boolean OR n.read_at IS NULL) ORDER BY n.id DESC LIMIT $5`, me(r).ID, me(r).Role, before, unreadOnly, limit+1)
	if err != nil {
		errDB(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var doc, task, kind, title, body string
		var created time.Time
		var read *time.Time
		if err = rows.Scan(&id, &doc, &task, &kind, &title, &body, &created, &read); err != nil {
			rows.Close()
			errDB(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "document_id": doc, "task_id": task, "kind": kind, "title": title, "body": body, "created_at": created, "read_at": read})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		errDB(w, err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1]["id"]
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "unread_count": unread, "max_id": maxID, "next_cursor": next})
}
func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 400, "Некорректное уведомление")
		return
	}
	tag, err := s.db.Exec(r.Context(), "UPDATE notifications SET read_at=coalesce(read_at,now()) WHERE id=$1 AND user_id=$2", id, me(r).ID)
	if err != nil {
		errDB(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "Уведомление не найдено")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) readAllNotifications(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ThroughID int64 `json:"through_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ThroughID < 1 {
		fail(w, 400, "Укажите through_id из списка уведомлений")
		return
	}
	if _, err := s.db.Exec(r.Context(), "UPDATE notifications SET read_at=now() WHERE user_id=$1 AND id<=$2 AND read_at IS NULL", me(r).ID, in.ThroughID); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) generateReminders(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, kind := range []string{"due_soon", "overdue"} {
		condition := `d.due_at>now() AND d.due_at<=now()+make_interval(hours=>coalesce((u.notification_preferences->>'reminder_hours')::integer,24))`
		title := "Приближается срок задачи"
		if kind == "overdue" {
			condition = "d.due_at<=now()"
			title = "Просрочена задача"
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications(user_id,document_id,task_id,kind,title,body,deadline_at,dedupe_key)
   SELECT u.id,d.id,t.id,$1,$2||': '||t.title,d.title,d.due_at,$1||':'||t.id::text||':'||to_char(d.due_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US')
   FROM workflow_tasks t JOIN documents d ON d.id=t.document_id JOIN users u ON u.id=t.assignee_id JOIN workflow_instances wi ON wi.id=t.instance_id
   WHERE t.status IN ('pending','in_progress') AND wi.status='active' AND d.deleted_at IS NULL AND u.active=true AND `+condition+`
   AND coalesce((u.notification_preferences->>'enabled')::boolean,true) AND coalesce((u.notification_preferences->>$1)::boolean,true)
   ON CONFLICT(user_id,dedupe_key) DO NOTHING`, kind, title)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Server) notificationWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		run, cancel := context.WithTimeout(ctx, 25*time.Second)
		err := s.generateReminders(run)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("notification reminders: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
