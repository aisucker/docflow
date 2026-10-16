package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Use a dedicated empty database. The test never connects to the application's database.
func TestWorkflowIntegration(t *testing.T) {
	url := os.Getenv("DOCFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("DOCFLOW_TEST_DATABASE_URL is not configured")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "docflow_test_") {
		t.Fatal("integration tests require a docflow_test_ database")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	srv := &Server{db: pool, secret: []byte(strings.Repeat("s", 48)), storage: t.TempDir()}
	if err = srv.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	users := []string{}
	for i := 0; i < 3; i++ {
		var id string
		err = pool.QueryRow(context.Background(), "INSERT INTO users(email,password_hash,full_name,role) VALUES($1,'unused',$1,$2) RETURNING id", fmt.Sprintf("test%d@local", i), []string{"admin", "employee", "employee"}[i]).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
	}
	var typ string
	err = pool.QueryRow(context.Background(), "INSERT INTO document_types(code,name) VALUES('test','Test') RETURNING id").Scan(&typ)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.routes()
	call := func(user int, method, path string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+srv.makeAccess(users[user]))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	object := func(b []byte) map[string]any {
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	seq := 0
	route := func(f Flow) string {
		seq++
		return object(call(0, "POST", "/admin/workflows", flowInput{Key: fmt.Sprintf("test_%d", seq), Name: "Test route", Definition: f}, 201))["id"].(string)
	}
	document := func() string {
		return object(call(0, "POST", "/documents", documentInput{TypeID: typ, Title: "Test", Fields: map[string]any{}}, 201))["id"].(string)
	}
	start := func(id, wf string) {
		call(0, "POST", "/documents/"+id+"/start", map[string]string{"workflow_id": wf}, 201)
	}
	tasks := func(id string) []map[string]any {
		var v []map[string]any
		json.Unmarshal(call(0, "GET", "/documents/"+id+"/route", nil, 200), &v)
		return v
	}
	task := func(id string, user, step int) string {
		for _, v := range tasks(id) {
			if v["assignee_id"] == users[user] && int(v["step_index"].(float64)) == step {
				return v["id"].(string)
			}
		}
		t.Fatal("task missing")
		return ""
	}
	status := func(id, want string) {
		got := object(call(0, "GET", "/documents/"+id, nil, 200))["status"]
		if got != want {
			t.Fatalf("document status %v want %s", got, want)
		}
	}
	decide := func(user int, id, code, comment string, want int) {
		call(user, "POST", "/tasks/"+id+"/decision", map[string]string{"action_code": code, "comment": comment}, want)
	}
	step := func(id, kind, mode string, assignees []string, buttons ...string) Step {
		return Step{ID: id, Title: id, Kind: kind, Mode: mode, AssigneeIDs: assignees, ButtonCodes: buttons}
	}

	t.Run("catalog permissions and custom actions", func(t *testing.T) {
		call(1, "GET", "/admin/buttons", nil, 403)
		call(0, "POST", "/admin/buttons", WorkflowButton{Code: "deliver", Label: "Ответ отправлен", Effect: "complete", Style: "primary", CommentRequired: true, Active: true}, 200)
		call(0, "POST", "/admin/buttons", WorkflowButton{Code: "deliver", Label: "Duplicate", Effect: "complete", Style: "primary", Active: true}, 409)
	})
	t.Run("save reload versions and immutable snapshots", func(t *testing.T) {
		f := Flow{CompletionStatus: "completed", Steps: []Step{step("execute", "task", "all", users[1:2], "start", "deliver", "return")}}
		wf := route(f)
		saved := object(call(0, "GET", "/workflows/"+wf, nil, 200))
		if saved["version"] != float64(1) {
			t.Fatal("version missing")
		}
		id := document()
		start(id, wf)
		call(0, "PUT", "/admin/buttons/deliver", WorkflowButton{Label: "Changed", Effect: "reject", Style: "subtle", Active: true}, 200)
		v2 := object(call(0, "PUT", "/admin/workflows/"+wf, map[string]any{"name": "New", "definition": Flow{Steps: []Step{step("done", "task", "all", users[1:2], "complete")}, CompletionStatus: "completed"}}, 201))["id"].(string)
		call(0, "PUT", "/admin/workflows/"+wf, map[string]any{"name": "Stale", "definition": Flow{Steps: []Step{step("done", "task", "all", users[1:2], "complete")}}}, 409)
		call(0, "PUT", "/admin/workflows/"+v2, map[string]any{"name": "Third", "definition": Flow{Steps: []Step{step("done", "task", "all", users[1:2], "complete")}}}, 201)
		tid := task(id, 1, 0)
		decide(2, tid, "deliver", "test", 403)
		decide(1, tid, "approve", "", 400)
		decide(1, tid, "deliver", "", 400)
		decide(1, tid, "start", "", 200)
		status(id, "in_progress")
		decide(1, tid, "start", "", 409)
		var listed []any
		json.Unmarshal(call(1, "GET", "/tasks", nil, 200), &listed)
		if len(listed) == 0 {
			t.Fatal("in-progress task disappeared")
		}
		var inbox []map[string]any
		json.Unmarshal(call(1, "GET", "/documents?journal=inbox", nil, 200), &inbox)
		found := false
		for _, d := range inbox {
			if d["id"] == id {
				found = true
			}
		}
		if !found {
			t.Fatal("in-progress document disappeared from inbox")
		}
		decide(1, tid, "deliver", "Sent", 200)
		status(id, "completed")
		decide(1, tid, "deliver", "Sent", 409)
	})
	t.Run("all assignees must finish before approval stage", func(t *testing.T) {
		wf := route(Flow{CompletionStatus: "approved", Steps: []Step{step("execute", "task", "all", users[1:], "start", "complete"), step("review", "approval", "any", users[:1], "approve", "reject")}})
		id := document()
		start(id, wf)
		decide(1, task(id, 1, 0), "start", "", 200)
		decide(2, task(id, 2, 0), "complete", "", 200)
		if len(tasks(id)) != 2 {
			t.Fatal("advanced before all completed")
		}
		decide(1, task(id, 1, 0), "complete", "", 200)
		status(id, "in_review")
		decide(0, task(id, 0, 1), "approve", "", 200)
		status(id, "approved")
	})
	t.Run("any completes cancels other in-progress task", func(t *testing.T) {
		wf := route(Flow{CompletionStatus: "completed", Steps: []Step{step("execute", "task", "any", users[1:], "start", "complete")}})
		id := document()
		start(id, wf)
		decide(2, task(id, 2, 0), "start", "", 200)
		decide(1, task(id, 1, 0), "complete", "", 200)
		status(id, "completed")
		decide(2, task(id, 2, 0), "complete", "", 409)
	})
	t.Run("return reject and restart", func(t *testing.T) {
		wf := route(Flow{Steps: []Step{step("execute", "task", "all", users[1:], "complete", "return", "reject")}})
		id := document()
		start(id, wf)
		decide(1, task(id, 1, 0), "return", "", 400)
		decide(1, task(id, 1, 0), "return", "Fix fields", 200)
		status(id, "returned")
		decide(2, task(id, 2, 0), "complete", "", 409)
		start(id, wf)
		var latest string
		for _, v := range tasks(id) {
			if v["status"] == "pending" && v["assignee_id"] == users[1] {
				latest = v["id"].(string)
			}
		}
		decide(1, latest, "reject", "Rejected", 200)
		status(id, "rejected")
	})
	t.Run("conditional skip and acknowledgement", func(t *testing.T) {
		skipped := step("skip", "task", "all", users[1:2], "complete")
		skipped.Condition = &Condition{Field: "missing", Operator: "eq", Value: true}
		wf := route(Flow{CompletionStatus: "completed", Steps: []Step{skipped, step("read", "acknowledgement", "all", users[1:2], "acknowledge")}})
		id := document()
		start(id, wf)
		if len(tasks(id)) != 1 {
			t.Fatal("conditional step did not skip")
		}
		decide(1, task(id, 1, 1), "acknowledge", "", 200)
		status(id, "completed")
	})
	t.Run("reject dead end and inactive buttons", func(t *testing.T) {
		f := Flow{Steps: []Step{step("execute", "task", "all", users[1:2], "start")}}
		call(0, "POST", "/admin/workflows", flowInput{Key: "dead_end", Name: "Bad", Definition: f}, 400)
		call(0, "PUT", "/admin/buttons/complete", WorkflowButton{Label: "Done", Effect: "complete", Style: "primary", Active: false}, 200)
		f.Steps[0].ButtonCodes = []string{"complete"}
		call(0, "POST", "/admin/workflows", flowInput{Key: "inactive", Name: "Bad", Definition: f}, 400)
		call(0, "PUT", "/admin/buttons/complete", WorkflowButton{Label: "Done", Effect: "complete", Style: "primary", Active: true}, 200)
	})
	t.Run("concurrent completion advances exactly once", func(t *testing.T) {
		wf := route(Flow{Steps: []Step{step("execute", "task", "all", users[1:], "complete"), step("review", "approval", "all", users[:1], "approve")}})
		id := document()
		start(id, wf)
		tids := []string{task(id, 1, 0), task(id, 2, 0)}
		var wg sync.WaitGroup
		codes := make(chan int, 2)
		for i, tid := range tids {
			wg.Add(1)
			go func(user int, tid string) {
				defer wg.Done()
				r := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+tid+"/decision", strings.NewReader(`{"action_code":"complete"}`))
				r.Header.Set("Authorization", "Bearer "+srv.makeAccess(users[user]))
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				codes <- w.Code
			}(i+1, tid)
		}
		wg.Wait()
		close(codes)
		for c := range codes {
			if c != 200 {
				t.Fatalf("concurrent decision: %d", c)
			}
		}
		if len(tasks(id)) != 3 {
			t.Fatal("next stage created more than once")
		}
	})
	t.Run("legacy instance retains approval buttons", func(t *testing.T) {
		wf := route(Flow{Steps: []Step{{ID: "legacy", Title: "Legacy", Mode: "all", AssigneeIDs: users[1:2]}}})
		id := document()
		start(id, wf)
		raw, _ := json.Marshal(Flow{Steps: []Step{{ID: "legacy", Title: "Legacy", Mode: "all", AssigneeIDs: users[1:2]}}})
		_, err := pool.Exec(context.Background(), "UPDATE workflow_instances SET definition_snapshot=$1 WHERE document_id=$2", raw, id)
		if err != nil {
			t.Fatal(err)
		}
		call(1, "POST", "/tasks/"+task(id, 1, 0)+"/decision", map[string]string{"decision": "approve"}, 200)
		status(id, "approved")
	})
}
