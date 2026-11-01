package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceValidation(t *testing.T) {
	p := Principal{ID: "00000000-0000-4000-8000-000000000001", Role: "employee"}
	for _, query := range []string{"sort=title%3BDROP+TABLE+documents", "author_id=x", "priority=secret", "status=unknown", "due=never", "limit=0", "offset=-1", "created_from=2026-02-30"} {
		q, _ := url.ParseQuery(query)
		if _, err := buildDocumentFilter(p, q, 100); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	q := url.Values{"q": {"' OR true--"}, "tag": {"закупка"}}
	f, err := buildDocumentFilter(p, q, 100)
	if err != nil || strings.Contains(f.where, "true--") || len(f.args) != 4 {
		t.Fatalf("filter must bind parameters: %+v %v", f, err)
	}
	for _, value := range []string{"=SUM(A1)", " +1", "\tcmd", "@formula", "-1"} {
		if safeCSV(value) != "'"+value {
			t.Errorf("unsafe CSV: %q", value)
		}
	}
	if safeCSV("") != "" || safeCSV("Документ") != "Документ" {
		t.Fatal("ordinary CSV values changed")
	}
	tags, err := normalizeTags([]string{" ВАЖНО ", "важно", "договор"})
	if err != nil || len(tags) != 2 || tags[0] != "важно" {
		t.Fatalf("normalize: %v %v", tags, err)
	}
}

func TestWorkspaceIntegration(t *testing.T) {
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
	s := &Server{db: pool, secret: []byte(strings.Repeat("w", 48)), storage: t.TempDir()}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	users := []string{}
	for i := 0; i < 4; i++ {
		var id string
		role := "employee"
		if i == 0 {
			role = "admin"
		}
		if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash,full_name,role) VALUES($1,'unused',$1,$2) RETURNING id", fmt.Sprintf("workspace%d@test.local", i), role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
	}
	var typ string
	if err = pool.QueryRow(ctx, "INSERT INTO document_types(code,name) VALUES('workspace_test','Workspace') RETURNING id").Scan(&typ); err != nil {
		t.Fatal(err)
	}
	handler := s.routes()
	call := func(user int, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+s.makeAccess(users[user]))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	obj := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var v map[string]any
		if json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatal(w.Body.String())
		}
		return v
	}
	list := func(w *httptest.ResponseRecorder) []map[string]any {
		t.Helper()
		var v []map[string]any
		if json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatal(w.Body.String())
		}
		return v
	}
	sql := func(query string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, query, args...); e != nil {
			t.Fatal(e)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if e := pool.QueryRow(ctx, query, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	create := func(user int, title string, assignee string) string {
		t.Helper()
		return obj(call(user, "POST", "/documents", documentInput{TypeID: typ, Title: title, AssigneeID: assignee, Fields: map[string]any{}}, 201))["id"].(string)
	}
	a := create(1, "workspace alpha", users[2])
	b := create(1, "workspace beta", "")
	private := create(3, "workspace private", "")
	base := "/documents/" + a
	// Personal favorites never grant access and survive filtering.
	call(1, "PUT", base+"/favorite", map[string]any{"favorite": true}, 200)
	if len(list(call(1, "GET", "/documents?favorite=true", nil, 200))) != 1 || len(list(call(2, "GET", "/documents?favorite=true", nil, 200))) != 0 {
		t.Fatal("favorites not personal")
	}
	call(1, "PUT", "/documents/"+private+"/favorite", map[string]any{"favorite": true}, 404)
	workspace := obj(call(1, "GET", base+"/workspace", nil, 200))
	if workspace["can_edit"] != true || workspace["favorite"] != true {
		t.Fatal(workspace)
	}
	stamp := workspace["updated_at"]
	call(1, "PATCH", base+"/metadata", map[string]any{"priority": "urgent", "tags": []string{" Закупка ", "важно"}, "expected_updated_at": stamp}, 200)
	call(1, "PATCH", base+"/metadata", map[string]any{"priority": "low", "expected_updated_at": stamp}, 409)
	if count("SELECT count(*) FROM document_revisions WHERE document_id=$1", a) != 2 {
		t.Fatal("one metadata action must make exactly one revision")
	}
	if len(list(call(1, "GET", "/documents?priority=urgent&tag="+url.QueryEscape("закупка"), nil, 200))) != 1 {
		t.Fatal("combined advanced filters")
	}
	call(1, "GET", "/documents?sort=bad", nil, 400)
	// Views are private, persist full filters and reject unknown parameters.
	view := obj(call(1, "POST", "/workspace/views", map[string]any{"name": "Срочные закупки", "filters": map[string]string{"priority": "urgent", "tag": "закупка"}}, 201))["id"].(string)
	if len(list(call(1, "GET", "/workspace/views", nil, 200))) != 1 || len(list(call(2, "GET", "/workspace/views", nil, 200))) != 0 {
		t.Fatal("views not personal")
	}
	call(2, "DELETE", "/workspace/views/"+view, nil, 404)
	call(1, "POST", "/workspace/views", map[string]any{"name": "invalid", "filters": map[string]string{"secret": "x"}}, 400)
	// Comments / mentions validate the recipient's existing access atomically.
	call(1, "POST", base+"/comments", map[string]any{"body": "Проверьте", "mentions": []string{users[3]}}, 400)
	if count("SELECT count(*) FROM document_comments WHERE document_id=$1", a) != 0 {
		t.Fatal("invalid mention comment was committed")
	}
	call(1, "POST", base+"/comments", map[string]any{"body": "Проверьте реквизиты", "mentions": []string{users[2]}}, 200)
	if count("SELECT count(*) FROM notifications WHERE document_id=$1 AND user_id=$2 AND kind='mention'", a, users[2]) != 1 {
		t.Fatal("mention notification missing")
	}
	call(2, "PUT", "/me/notification-preferences", map[string]any{"mentions": false}, 200)
	call(1, "POST", base+"/comments", map[string]any{"body": "Без уведомления", "mentions": []string{users[2]}}, 200)
	if count("SELECT count(*) FROM notifications WHERE document_id=$1 AND user_id=$2 AND kind='mention'", a, users[2]) != 1 {
		t.Fatal("mention opt-out ignored")
	}
	call(3, "GET", base+"/workspace", nil, 404)
	call(3, "POST", base+"/comments", map[string]any{"body": "no access"}, 404)
	// An admin can read all; a reader on a workflow cannot edit metadata/checklist.
	sql("UPDATE documents SET assignee_id=$1 WHERE id=$2", users[2], b)
	call(1, "POST", base+"/checklist", map[string]string{"label": "Проверить реквизиты"}, 200)
	ws := obj(call(2, "GET", base+"/workspace", nil, 200))
	item := ws["checklist"].([]any)[0].(map[string]any)["id"].(string)
	call(2, "PATCH", base+"/checklist/"+item, map[string]bool{"done": true}, 200)
	call(1, "PATCH", "/documents/"+b+"/checklist/"+item, map[string]bool{"done": false}, 404)
	if count("SELECT count(*) FROM document_checklist WHERE id=$1 AND done AND completed_by=$2", item, users[2]) != 1 {
		t.Fatal("checklist completion")
	}
	// Links are bidirectional but both ends still obey read ACL.
	call(1, "POST", base+"/links", map[string]string{"target_id": private, "kind": "basis"}, 404)
	call(1, "POST", base+"/links", map[string]string{"target_id": b, "kind": "basis"}, 200)
	if len(obj(call(1, "GET", "/documents/"+b+"/workspace", nil, 200))["links"].([]any)) != 1 {
		t.Fatal("incoming link missing")
	}
	call(0, "POST", base+"/links", map[string]string{"target_id": private, "kind": "related"}, 200)
	ws = obj(call(1, "GET", base+"/workspace", nil, 200))
	if len(ws["links"].([]any)) != 1 {
		t.Fatal("private linked document leaked")
	}
	// Duplicate is a clean draft, with current fields but no registration/tasks/files.
	sql("UPDATE documents SET registration_number='WS-001',status='approved' WHERE id=$1", a)
	sql("INSERT INTO files(document_id,filename,mime_type,storage_key,size,version,uploaded_by) VALUES($1,'x.txt','text/plain','test',1,1,$2)", a, users[1])
	copyID := obj(call(2, "POST", base+"/duplicate", map[string]any{}, 201))["id"].(string)
	if count("SELECT count(*) FROM documents WHERE id=$1 AND author_id=$2 AND status='draft' AND registration_number IS NULL AND project_number IS NULL AND priority='urgent'", copyID, users[2]) != 1 || count("SELECT count(*) FROM files WHERE document_id=$1", copyID) != 0 || count("SELECT count(*) FROM workflow_tasks WHERE document_id=$1", copyID) != 0 {
		t.Fatal("copy carries execution data")
	}
	// Bulk results report each item independently, without changing forbidden docs.
	results := obj(call(1, "POST", "/workspace/bulk", map[string]any{"ids": []string{a, b, private}, "action": "tag_add", "tag": "пакет"}, 200))["results"].([]any)
	success := 0
	for _, result := range results {
		if result.(map[string]any)["ok"] == true {
			success++
		}
	}
	if success != 2 {
		t.Fatal(results)
	}
	if count("SELECT count(*) FROM documents WHERE id=$1 AND 'пакет'=ANY(tags)", private) != 0 {
		t.Fatal("unauthorized bulk mutation")
	}
	// All collection APIs and CSV use identical document permissions.
	sql("UPDATE documents SET title='=SUM(A1)',due_at='2026-10-31T19:01:00Z' WHERE id=$1", b)
	sql("UPDATE documents SET due_at='2026-11-30T18:59:00Z' WHERE id=$1", a)
	sql("UPDATE documents SET due_at='2026-11-01T12:00:00Z' WHERE id=$1", private)
	csv := call(1, "GET", "/documents/export?q=workspace", nil, 200).Body.String()
	if strings.Contains(csv, "private") {
		t.Fatal("CSV leaks private doc")
	}
	csv = call(1, "GET", "/documents/export?type_id="+typ, nil, 200).Body.String()
	if !strings.Contains(csv, "'=SUM(A1)") {
		t.Fatal("CSV formula not escaped")
	}
	cal := obj(call(1, "GET", "/workspace/calendar?month=2026-11&type_id="+typ, nil, 200))
	if len(cal["items"].([]any)) != 2 {
		t.Fatal(cal)
	}
	call(1, "GET", "/workspace/calendar?month=wrong", nil, 400)
	stats := obj(call(1, "GET", "/workspace/analytics?type_id="+typ, nil, 200))
	if stats["total"] != float64(2) {
		t.Fatal(stats)
	}
	if obj(call(3, "GET", "/workspace/analytics?type_id="+typ, nil, 200))["total"] != float64(1) {
		t.Fatal("analytics ACL")
	}
	// Versions expose changes with actor identity; no revision for an unchanged card.
	revisions := list(call(1, "GET", base+"/versions", nil, 200))
	last := int(revisions[0]["version"].(float64))
	sql("UPDATE documents SET title=title WHERE id=$1", a)
	if count("SELECT count(*) FROM document_revisions WHERE document_id=$1", a) != last {
		t.Fatal("unchanged update created version")
	}
	comparison := obj(call(1, "GET", base+"/versions/compare?from=1&to=2", nil, 200))
	if len(comparison["changes"].([]any)) != 2 {
		t.Fatal(comparison)
	}
	if count("SELECT count(*) FROM document_revisions WHERE document_id=$1 AND version=2 AND editor_id=$2", a, users[1]) != 1 {
		t.Fatal("revision editor missing")
	}
	call(3, "GET", base+"/versions", nil, 404)
	call(1, "DELETE", base+"/checklist/"+item, nil, 200)
	call(1, "DELETE", "/workspace/views/"+view, nil, 200)
	// Pagination includes older records, rather than silently truncating at 100.
	sql("INSERT INTO documents(type_id,title,author_id,created_at) SELECT $1,'paging '||i,$2,now()-i*interval '1 minute' FROM generate_series(1,105) i", typ, users[1])
	page := list(call(1, "GET", "/documents?q=paging&limit=10&offset=100&sort=oldest", nil, 200))
	if len(page) != 5 {
		t.Fatal("pagination", len(page))
	}
	// Seed a read-only workflow participant to verify metadata ACL separately.
	var definition, instance string
	if err = pool.QueryRow(ctx, "INSERT INTO workflow_definitions(workflow_key,version,name,definition,created_by) VALUES('workspace_reader',1,'Reader','{}',$1) RETURNING id", users[0]).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO workflow_instances(document_id,definition_id,definition_snapshot,started_by) VALUES($1,$2,'{}',$3) RETURNING id", a, definition, users[1]).Scan(&instance); err != nil {
		t.Fatal(err)
	}
	sql("INSERT INTO workflow_tasks(instance_id,document_id,step_index,step_id,title,assignee_id) VALUES($1,$2,0,'read','Read',$3)", instance, a, users[3])
	call(3, "GET", base+"/workspace", nil, 200)
	call(3, "PATCH", base+"/metadata", map[string]any{"priority": "high", "expected_updated_at": time.Now().Format(time.RFC3339Nano)}, 403)
	call(3, "POST", base+"/checklist", map[string]string{"label": "not allowed"}, 403)
}
