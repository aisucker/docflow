package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestJournalFormat(t *testing.T) {
	v := Journal{Name: "Исходящие", Kind: "registration", Pattern: "ИСХ-{seq}/{year}", Width: 4, Next: 17, Active: true}
	if msg := validateJournal(v); msg != "" {
		t.Fatal(msg)
	}
	if got := formatJournalNumber(v, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); got != "ИСХ-0017/2026" {
		t.Fatal(got)
	}
	for _, pattern := range []string{"fixed", "{seq}-{seq}", "{seq}/{month}", "{seq}\n"} {
		v.Pattern = pattern
		if validateJournal(v) == "" {
			t.Fatalf("accepted %q", pattern)
		}
	}
}

func TestJournalIntegration(t *testing.T) {
	url := os.Getenv("DOCFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("dedicated test database required")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "docflow_test_") {
		t.Fatal("test database required")
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	srv := &Server{db: pool, secret: []byte(strings.Repeat("j", 48)), storage: t.TempDir()}
	if err = srv.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	users := []string{}
	for i := 0; i < 2; i++ {
		var id string
		if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash,full_name,role) VALUES($1,'unused',$1,$2) RETURNING id", fmt.Sprintf("journal%d@local", i), []string{"admin", "employee"}[i]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
	}
	handler := srv.routes()
	rawCall := func(user int, method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+srv.makeAccess(users[user]))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	call := func(user int, method, path string, body any, want int) []byte {
		t.Helper()
		w := rawCall(user, method, path, body)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	object := func(raw []byte) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	project := Journal{Name: "Проекты", Kind: "project", Pattern: "ПР-{seq}/{year}", Width: 4, Next: 1, Active: true}
	reg := Journal{Name: "Регистрация", Kind: "registration", Pattern: "РЕГ-{seq}/{year}", Width: 3, Next: 10, Active: true}
	call(1, "POST", "/admin/journals", project, 403)
	project.ID = object(call(0, "POST", "/admin/journals", project, 201))["id"].(string)
	reg.ID = object(call(0, "POST", "/admin/journals", reg, 201))["id"].(string)
	call(1, "GET", "/journals/"+reg.ID+"/entries", nil, 403)
	typ := docTypeInput{Code: "journal_test", Name: "Журнальный документ", ProjectJournalID: project.ID, RegistrationJournalID: reg.ID}
	typID := object(call(0, "POST", "/admin/document-types", typ, 201))["id"].(string)
	invalidType := typ
	invalidType.ProjectJournalID = reg.ID
	call(0, "PUT", "/admin/document-types/"+typID, invalidType, 400)
	flowSeq := 0
	route := func(steps []Step) {
		t.Helper()
		flowSeq++
		key := fmt.Sprintf("journal_flow_%d", flowSeq)
		call(0, "POST", "/admin/workflows", flowInput{Key: key, Name: key, Definition: Flow{Steps: steps, CompletionStatus: "completed"}}, 201)
		typ.DefaultWorkflowKey = key
		call(0, "PUT", "/admin/document-types/"+typID, typ, 200)
	}
	create := func() string {
		t.Helper()
		return object(call(0, "POST", "/documents", documentInput{TypeID: typID, Title: "Нумерация", Fields: map[string]any{}}, 201))["id"].(string)
	}
	get := func(id string) map[string]any { return object(call(0, "GET", "/documents/"+id, nil, 200)) }
	tasks := func(id string) []map[string]any {
		var result []map[string]any
		if err := json.Unmarshal(call(0, "GET", "/documents/"+id+"/route", nil, 200), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	step := Step{ID: "registration", Title: "Регистрация", Kind: "registration", Mode: "all", AssigneeIDs: users}
	route([]Step{step})
	call(0, "POST", "/documents", documentInput{TypeID: typID, Title: "Bad", RegistrationNumber: "manual"}, 400)
	id := create()
	if get(id)["project_number"] != "" || get(id)["registration_number"] != "" {
		t.Fatal("draft numbered")
	}
	call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
	d := get(id)
	projectNumber := d["project_number"].(string)
	if projectNumber == "" || d["registration_number"] != "" {
		t.Fatal(d)
	}
	routeTasks := tasks(id)
	for _, task := range routeTasks {
		who := 0
		if task["assignee_id"] == users[1] {
			who = 1
		}
		call(who, "POST", "/tasks/"+task["id"].(string)+"/decision", map[string]string{"action_code": "register"}, 200)
		if task["id"] == routeTasks[0]["id"] && get(id)["registration_number"] != "" {
			t.Fatal("all stage registered before last executor")
		}
	}
	d = get(id)
	if !strings.HasPrefix(d["registration_number"].(string), "РЕГ-010/") || d["registered_at"] == nil || d["project_number"] != projectNumber {
		t.Fatal(d)
	}
	call(0, "POST", "/tasks/"+routeTasks[0]["id"].(string)+"/decision", map[string]string{"action_code": "register"}, 409)
	reg.Next = 10
	call(0, "PUT", "/admin/journals/"+reg.ID, reg, 409)
	call(0, "DELETE", "/documents/"+id, nil, 204)
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM journal_entries WHERE document_id=$1", id).Scan(&count); err != nil || count != 2 {
		t.Fatal("deleted document lost its numbers", err, count)
	}

	t.Run("return restart and disabled journal rollback", func(t *testing.T) {
		step.AssigneeIDs = users[:1]
		route([]Step{step})
		id := create()
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
		before := get(id)["project_number"]
		task := tasks(id)[0]["id"].(string)
		call(0, "POST", "/tasks/"+task+"/decision", map[string]string{"action_code": "return", "comment": "Исправить"}, 200)
		if get(id)["registration_number"] != "" {
			t.Fatal("return registered document")
		}
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
		if get(id)["project_number"] != before {
			t.Fatal("restart changed project number")
		}
		plan := object(call(0, "GET", "/documents/"+id+"/workflow", nil, 200))
		if plan["instance_id"] == "" || plan["instance_status"] != "active" || plan["current_step"] != float64(0) {
			t.Fatalf("missing latest route state: %v", plan)
		}
		instances := map[string]bool{}
		for _, v := range tasks(id) {
			instance, ok := v["instance_id"].(string)
			if !ok || instance == "" {
				t.Fatal("task instance missing")
			}
			instances[instance] = true
			if v["status"] == "pending" && instance != plan["instance_id"] {
				t.Fatal("plan does not describe latest instance")
			}
		}
		if len(instances) != 2 {
			t.Fatalf("expected both attempts in history: %v", instances)
		}
		for _, v := range tasks(id) {
			if v["status"] == "pending" {
				task = v["id"].(string)
			}
		}
		if _, err := pool.Exec(ctx, "UPDATE registration_journals SET active=false WHERE id=$1", reg.ID); err != nil {
			t.Fatal(err)
		}
		call(0, "POST", "/tasks/"+task+"/decision", map[string]string{"action_code": "register"}, 409)
		if get(id)["registration_number"] != "" {
			t.Fatal("failed registration assigned number")
		}
		if _, err := pool.Exec(ctx, "UPDATE registration_journals SET active=true WHERE id=$1", reg.ID); err != nil {
			t.Fatal(err)
		}
		call(0, "POST", "/tasks/"+task+"/decision", map[string]string{"action_code": "register"}, 200)
		if get(id)["registration_number"] == "" {
			t.Fatal("registration missing")
		}
	})

	t.Run("concurrent project and registration allocation", func(t *testing.T) {
		ids := []string{}
		for i := 0; i < 8; i++ {
			ids = append(ids, create())
		}
		var wg sync.WaitGroup
		results := make(chan *httptest.ResponseRecorder, len(ids))
		for _, id := range ids {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				results <- rawCall(0, "POST", "/documents/"+id+"/start", map[string]any{})
			}(id)
		}
		wg.Wait()
		close(results)
		for w := range results {
			if w.Code != 201 {
				t.Fatalf("concurrent start: %d %s", w.Code, w.Body.String())
			}
		}
		results = make(chan *httptest.ResponseRecorder, len(ids))
		seen := map[string]bool{}
		for _, id := range ids {
			num := get(id)["project_number"].(string)
			if seen[num] || num == "" {
				t.Fatal("duplicate project number", num)
			}
			seen[num] = true
			task := tasks(id)[0]["id"].(string)
			wg.Add(1)
			go func(task string) {
				defer wg.Done()
				results <- rawCall(0, "POST", "/tasks/"+task+"/decision", map[string]string{"action_code": "register"})
			}(task)
		}
		wg.Wait()
		close(results)
		for w := range results {
			if w.Code != 200 {
				t.Fatalf("concurrent registration: %d %s", w.Code, w.Body.String())
			}
		}
		seen = map[string]bool{}
		for _, id := range ids {
			num := get(id)["registration_number"].(string)
			if seen[num] || num == "" {
				t.Fatal("duplicate registration number", num)
			}
			seen[num] = true
		}
	})

	t.Run("parallel registration any and retained numbers on restart", func(t *testing.T) {
		step.Mode = "any"
		step.AssigneeIDs = users
		step.ParallelGroup = "parallel"
		other := Step{ID: "review", Title: "Проверка", Kind: "approval", Mode: "all", AssigneeIDs: users[:1], ParallelGroup: "parallel"}
		route([]Step{step, other})
		id := create()
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
		var regTask, reviewTask string
		for _, task := range tasks(id) {
			if task["step_id"] == "registration" && task["assignee_id"] == users[0] {
				regTask = task["id"].(string)
			}
			if task["step_id"] == "review" {
				reviewTask = task["id"].(string)
			}
		}
		call(0, "POST", "/tasks/"+regTask+"/decision", map[string]string{"action_code": "register"}, 200)
		number := get(id)["registration_number"]
		if number == "" || get(id)["status"] == "completed" {
			t.Fatal("parallel registration did not register independently")
		}
		call(0, "POST", "/tasks/"+reviewTask+"/decision", map[string]string{"action_code": "return", "comment": "Исправить"}, 200)
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
		for _, task := range tasks(id) {
			if task["status"] == "pending" && task["step_id"] == "registration" && task["assignee_id"] == users[0] {
				regTask = task["id"].(string)
			}
		}
		call(0, "POST", "/tasks/"+regTask+"/decision", map[string]string{"action_code": "register"}, 200)
		if get(id)["registration_number"] != number {
			t.Fatal("registration changed after restart")
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM journal_entries WHERE document_id=$1 AND kind='registration'", id).Scan(&count); err != nil || count != 1 {
			t.Fatal("duplicate registration entry", err, count)
		}
	})

	t.Run("failed next stage rolls back registration and counter", func(t *testing.T) {
		step.Mode = "all"
		step.AssigneeIDs = users[:1]
		step.ParallelGroup = ""
		other := Step{ID: "later", Title: "Следующий этап", Kind: "task", Mode: "all", AssigneeIDs: users[1:]}
		route([]Step{step, other})
		id := create()
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
		task := tasks(id)[0]["id"].(string)
		var before int64
		if err := pool.QueryRow(ctx, "SELECT next_number FROM registration_journals WHERE id=$1", reg.ID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE users SET active=false WHERE id=$1", users[1]); err != nil {
			t.Fatal(err)
		}
		call(0, "POST", "/tasks/"+task+"/decision", map[string]string{"action_code": "register"}, 409)
		if get(id)["registration_number"] != "" {
			t.Fatal("failed transition retained registration")
		}
		var after int64
		if err := pool.QueryRow(ctx, "SELECT next_number FROM registration_journals WHERE id=$1", reg.ID).Scan(&after); err != nil || after != before {
			t.Fatal("failed transition consumed number", err, before, after)
		}
		if _, err := pool.Exec(ctx, "UPDATE users SET active=true WHERE id=$1", users[1]); err != nil {
			t.Fatal(err)
		}
		call(0, "POST", "/tasks/"+task+"/decision", map[string]string{"action_code": "register"}, 200)
		if get(id)["registration_number"] == "" {
			t.Fatal("successful retry did not register")
		}
	})
	t.Run("signing requires identity verified challenge and unchanged document", func(t *testing.T) {
		valid := true
		expectedData := ""
		verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["data"] != expectedData {
				t.Error("signature verifier received wrong payload")
			}
			report := goodNCAReport()
			report["valid"] = valid
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(report)
		}))
		defer verifier.Close()
		t.Setenv("NCANODE_URL", verifier.URL)
		route([]Step{{ID: "signature", Title: "Подписание", Kind: "signing", Mode: "all", AssigneeIDs: users[:1]}})
		id := create()
		call(0, "GET", "/documents/"+id+"/signed-pdf", nil, 409)
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 201)
		task := tasks(id)[0]["id"].(string)
		call(1, "POST", "/tasks/"+task+"/signing-payload", map[string]any{}, 403)
		call(0, "POST", "/tasks/"+task+"/signing-payload", map[string]any{}, 409)
		call(0, "PATCH", "/admin/users/"+users[0], map[string]any{"role": "admin", "active": true, "signing_iin": "123456789012"}, 200)
		call(1, "PATCH", "/admin/users/"+users[1], map[string]any{"role": "employee", "active": true, "signing_iin": "123456789012"}, 403)
		call(0, "POST", "/tasks/"+task+"/decision", map[string]any{"action_code": "sign"}, 409)
		challenge := object(call(0, "POST", "/tasks/"+task+"/signing-payload", map[string]any{}, 201))
		expectedData = challenge["data"].(string)
		input := map[string]any{"action_code": "sign", "challenge_id": challenge["challenge_id"], "cms": base64.StdEncoding.EncodeToString(cmsFixture(false))}
		valid = false
		call(0, "POST", "/tasks/"+task+"/decision", input, 409)
		valid = true
		if _, err := pool.Exec(ctx, "UPDATE documents SET title='Changed' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		call(0, "POST", "/tasks/"+task+"/decision", input, 409)
		if _, err := pool.Exec(ctx, "UPDATE documents SET title='Нумерация' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		call(0, "POST", "/tasks/"+task+"/decision", input, 200)
		if get(id)["status"] != "completed" {
			t.Fatal("verified signature did not finish stage")
		}
		call(0, "POST", "/tasks/"+task+"/decision", input, 409)
		var signatures []map[string]any
		if err := json.Unmarshal(call(0, "GET", "/documents/"+id+"/signatures", nil, 200), &signatures); err != nil || len(signatures) != 1 {
			t.Fatal("signature not stored", err, signatures)
		}
		content := call(0, "GET", "/signatures/"+signatures[0]["id"].(string)+"/content?format=payload", nil, 200)
		if base64.StdEncoding.EncodeToString(content) != expectedData {
			t.Fatal("downloaded payload changed")
		}
		if os.Getenv("DOCJET_PDF_PYTHON") != "" {
			t.Setenv("PUBLIC_VERIFY_BASE_URL", "http://localhost:8081")
			pdf := call(0, "GET", "/documents/"+id+"/signed-pdf", nil, 200)
			if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
				t.Fatal("not a PDF")
			}
			var metadata []byte
			var hash string
			if err := pool.QueryRow(ctx, "SELECT metadata,pdf_sha256 FROM pdf_exports WHERE document_id=$1", id).Scan(&metadata, &hash); err != nil {
				t.Fatal(err)
			}
			if digest(pdf) != hash {
				t.Fatal("export hash mismatch")
			}
			var m pdfMetadata
			if json.Unmarshal(metadata, &m) != nil {
				t.Fatal("metadata JSON")
			}
			token := m.VerifyURL[len("http://localhost:8081/verify/"):]
			if !receiptToken.MatchString(token) {
				t.Fatal("weak verification token")
			}
			public := func(method, path string, want int) []byte {
				t.Helper()
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/public/verify/"+token+path, nil))
				if w.Code != want {
					t.Fatalf("verification %d want %d: %s", w.Code, want, w.Body.String())
				}
				return w.Body.Bytes()
			}
			result := public("GET", "", 200)
			if bytes.Contains(result, []byte("123456789012")) || bytes.Contains(result, []byte(`"fields"`)) || bytes.Contains(result, []byte(`"summary"`)) {
				t.Fatal("private signing data exposed")
			}
			public("POST", "/recheck", 200)
			public("POST", "/recheck", 429)
			if _, err := pool.Exec(ctx, "UPDATE pdf_exports SET checked_at=now()-interval '2 minutes' WHERE document_id=$1", id); err != nil {
				t.Fatal(err)
			}
			valid = false
			public("POST", "/recheck", 503)
			if bytes.Contains(public("GET", "", 200), []byte(`"check_valid":true`)) {
				t.Fatal("failed verification reported valid")
			}
			valid = true
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM signing_challenges WHERE task_id=$1 AND used_at IS NOT NULL", task).Scan(&count); err != nil || count != 1 {
			t.Fatal("challenge not consumed", err, count)
		}
	})
	t.Run("failed launch rolls back project number", func(t *testing.T) {
		route([]Step{{ID: "missing", Title: "Ответственный", Kind: "task", Mode: "all", Assignees: []AssigneeSource{{Kind: "assignee"}}}})
		id := create()
		var before int64
		if err := pool.QueryRow(ctx, "SELECT next_number FROM registration_journals WHERE id=$1", project.ID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		call(0, "POST", "/documents/"+id+"/start", map[string]any{}, 409)
		if get(id)["project_number"] != "" {
			t.Fatal("failed launch retained project number")
		}
		var after int64
		if err := pool.QueryRow(ctx, "SELECT next_number FROM registration_journals WHERE id=$1", project.ID).Scan(&after); err != nil || after != before {
			t.Fatal("failed launch consumed number", err, before, after)
		}
	})
}
