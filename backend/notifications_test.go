package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotificationDefaults(t *testing.T) {
	settings, err := notificationSettings([]byte(`{"task_assigned":false}`))
	if err != nil || settings.TaskAssigned || !settings.Enabled || !settings.TaskDelegated || settings.ReminderHours != 24 {
		t.Fatalf("bad defaults: %+v %v", settings, err)
	}
}

func TestNotificationsIntegration(t *testing.T) {
	dbURL := os.Getenv("DOCFLOW_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("dedicated test DB not configured")
	}
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "docflow_test_") {
		t.Fatal("requires a docflow_test_ database")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &Server{db: pool, secret: []byte(strings.Repeat("n", 48)), storage: t.TempDir()}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	users := []string{}
	for i := 0; i < 3; i++ {
		var id string
		err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash,full_name,role) VALUES($1,'unused',$1,$2) RETURNING id", fmt.Sprintf("notifications%d@test.local", i), []string{"admin", "employee", "employee"}[i]).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
	}
	var typ string
	if err = pool.QueryRow(ctx, "INSERT INTO document_types(code,name) VALUES('notification_test','Notifications') RETURNING id").Scan(&typ); err != nil {
		t.Fatal(err)
	}
	handler := s.routes()
	call := func(user int, method, path string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+s.makeAccess(users[user]))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var value map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &value)
		return value
	}
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(user int, doc, kind string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM notifications WHERE user_id=$1 AND document_id=$2 AND kind=$3", users[user], doc, kind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	seq := 0
	route := func(kind string) {
		t.Helper()
		seq++
		codes := []string{"complete"}
		if kind == "approval" {
			codes = []string{"approve", "return", "reject"}
		}
		key := fmt.Sprintf("notification_route_%d", seq)
		call(0, "POST", "/admin/workflows", flowInput{Key: key, Name: "Notification route", Definition: Flow{CompletionStatus: "completed", Steps: []Step{{ID: "work_stage", Title: "Проверить документ", Kind: kind, Mode: "all", AssigneeIDs: users[1:2], ButtonCodes: codes}}}}, 201)
		sql("UPDATE document_types SET default_workflow_key=$1 WHERE id=$2", key, typ)
	}
	document := func(due *time.Time) string {
		t.Helper()
		id := call(0, "POST", "/documents", documentInput{TypeID: typ, Title: "Документ для уведомлений", AssigneeID: users[0], DueAt: due, Fields: map[string]any{}}, 201)["id"].(string)
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
		return id
	}
	task := func(doc string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, "SELECT id FROM workflow_tasks WHERE document_id=$1 AND assignee_id=$2 AND status='pending'", doc, users[1]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	reminders := func() {
		t.Helper()
		if err := s.generateReminders(ctx); err != nil {
			t.Fatal(err)
		}
	}
	list := func(user int) []any { return call(user, "GET", "/notifications?limit=100", nil, 200)["items"].([]any) }
	has := func(items []any, doc, kind string) bool {
		for _, v := range items {
			item := v.(map[string]any)
			if item["document_id"] == doc && item["kind"] == kind {
				return true
			}
		}
		return false
	}

	t.Run("individual settings preserve interface preferences", func(t *testing.T) {
		prefs := call(1, "GET", "/me/notification-preferences", nil, 200)
		if prefs["enabled"] != true || prefs["reminder_hours"] != float64(24) {
			t.Fatal(prefs)
		}
		call(1, "PUT", "/me/preferences", map[string]any{"compact": true, "start_page": "tasks"}, 200)
		prefs = call(1, "PUT", "/me/notification-preferences", map[string]any{"task_assigned": false, "reminder_hours": 4}, 200)
		if prefs["task_assigned"] != false || prefs["overdue"] != true {
			t.Fatal("partial settings lost defaults")
		}
		if call(0, "GET", "/me/notification-preferences", nil, 200)["task_assigned"] != true {
			t.Fatal("settings leaked to another user")
		}
		if call(1, "GET", "/me", nil, 200)["preferences"].(map[string]any)["compact"] != true {
			t.Fatal("interface settings overwritten")
		}
		for _, bad := range []any{map[string]any{"reminder_hours": 0}, map[string]any{"reminder_hours": 169}, map[string]any{"enabled": nil}, map[string]any{"sms": true}, map[string]any{"task_assigned": "yes"}} {
			call(1, "PUT", "/me/notification-preferences", bad, 400)
		}
		route("task")
		doc := document(nil)
		if count(1, doc, "task_assigned") != 0 {
			t.Fatal("opt-out ignored")
		}
		call(1, "PUT", "/me/notification-preferences", map[string]any{"task_assigned": true}, 200)
	})
	t.Run("assignment deduplication and transaction rollback", func(t *testing.T) {
		route("task")
		doc := document(nil)
		if count(1, doc, "task_assigned") != 1 {
			t.Fatal("assignment missing")
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = event(ctx, tx, doc, "", "workflow.step_started", map[string]any{"step": "work_stage"}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if count(1, doc, "task_assigned") != 1 {
			t.Fatal("assignment duplicated")
		}
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = insertNotification(ctx, tx, users[2], doc, "", "task_completed", "Rollback", "Test", "rollback_test"); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
		if count(2, doc, "task_completed") != 0 {
			t.Fatal("rolled-back notification visible")
		}
	})
	t.Run("reminders respect deadlines opt-outs and completion", func(t *testing.T) {
		route("task")
		due := time.Now().Add(3 * time.Hour)
		doc := document(&due)
		reminders()
		reminders()
		if count(1, doc, "due_soon") != 1 {
			t.Fatal("reminder duplicated or missing")
		}
		sql("UPDATE documents SET due_at=now()-interval '1 hour' WHERE id=$1", doc)
		reminders()
		reminders()
		if count(1, doc, "overdue") != 1 {
			t.Fatal("overdue notification duplicated or missing")
		}
		if has(list(1), doc, "due_soon") {
			t.Fatal("outdated upcoming reminder still shown")
		}
		sql("UPDATE documents SET due_at=now()+interval '6 hours' WHERE id=$1", doc)
		reminders()
		if count(1, doc, "due_soon") != 1 {
			t.Fatal("reminded outside personal window")
		}
		call(1, "PUT", "/me/notification-preferences", map[string]any{"reminder_hours": 8}, 200)
		reminders()
		if count(1, doc, "due_soon") != 2 {
			t.Fatal("new deadline did not get reminder")
		}
		call(1, "POST", "/tasks/"+task(doc)+"/decision", map[string]string{"action_code": "complete"}, 200)
		if has(list(1), doc, "due_soon") || has(list(1), doc, "overdue") {
			t.Fatal("completed-task reminders still shown")
		}
		call(1, "PUT", "/me/notification-preferences", map[string]any{"due_soon": false}, 200)
		due = time.Now().Add(time.Hour)
		muted := document(&due)
		reminders()
		if count(1, muted, "due_soon") != 0 {
			t.Fatal("reminder opt-out ignored")
		}
		call(1, "PUT", "/me/notification-preferences", map[string]any{"enabled": false}, 200)
		muted = document(&due)
		reminders()
		if count(1, muted, "task_assigned") != 0 || count(1, muted, "due_soon") != 0 {
			t.Fatal("master switch ignored")
		}
		call(1, "PUT", "/me/notification-preferences", map[string]any{"enabled": true, "due_soon": true}, 200)
	})
	t.Run("ownership read filters cursor and read-all boundary", func(t *testing.T) {
		route("task")
		doc := document(nil)
		var notificationID int64
		if err := pool.QueryRow(ctx, "SELECT id FROM notifications WHERE user_id=$1 AND document_id=$2", users[1], doc).Scan(&notificationID); err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/notifications/%d/read", notificationID)
		call(2, "POST", path, nil, 404)
		call(1, "POST", path, nil, 200)
		call(1, "POST", path, nil, 200)
		if has(call(1, "GET", "/notifications?unread_only=true&limit=100", nil, 200)["items"].([]any), doc, "task_assigned") {
			t.Fatal("read notification in unread list")
		}
		first := call(1, "GET", "/notifications?limit=1", nil, 200)
		cursor := first["next_cursor"].(float64)
		second := call(1, "GET", fmt.Sprintf("/notifications?limit=1&before_id=%.0f", cursor), nil, 200)
		if first["items"].([]any)[0].(map[string]any)["id"] == second["items"].([]any)[0].(map[string]any)["id"] {
			t.Fatal("cursor repeated item")
		}
		boundary := call(1, "GET", "/notifications", nil, 200)["max_id"]
		newest := document(nil)
		call(1, "POST", "/notifications/read-all", map[string]any{"through_id": boundary}, 200)
		if !has(call(1, "GET", "/notifications?unread_only=true&limit=100", nil, 200)["items"].([]any), newest, "task_assigned") {
			t.Fatal("read-all consumed a newer notification")
		}
		call(1, "GET", "/notifications?limit=-1", nil, 400)
		call(1, "GET", "/notifications?before_id=bad", nil, 400)
		sql("UPDATE documents SET deleted_at=now() WHERE id=$1", newest)
		if has(list(1), newest, "task_assigned") {
			t.Fatal("deleted document leaked")
		}
	})
	t.Run("delegation outcomes and another user notifications", func(t *testing.T) {
		route("task")
		doc := document(nil)
		delegated := call(1, "POST", "/tasks/"+task(doc)+"/delegate", map[string]string{"assignee_id": users[2], "comment": "Проверьте"}, 201)["id"].(string)
		if count(2, doc, "task_delegated") != 1 {
			t.Fatal("delegation notification missing")
		}
		if has(list(1), doc, "task_delegated") {
			t.Fatal("another recipient notification leaked")
		}
		call(2, "POST", "/tasks/"+delegated+"/decision", map[string]string{"action_code": "complete"}, 200)
		if count(0, doc, "document_completed") != 1 || count(0, doc, "task_completed") != 1 {
			t.Fatal("document owner missing outcome")
		}
		route("approval")
		doc = document(nil)
		call(1, "POST", "/tasks/"+task(doc)+"/decision", map[string]string{"action_code": "return", "comment": "Исправить"}, 200)
		if count(0, doc, "document_returned") != 1 {
			t.Fatal("return notification missing")
		}
		doc = document(nil)
		call(1, "POST", "/tasks/"+task(doc)+"/decision", map[string]string{"action_code": "reject", "comment": "Не согласовано"}, 200)
		if count(0, doc, "document_rejected") != 1 {
			t.Fatal("rejection notification missing")
		}
	})
}
