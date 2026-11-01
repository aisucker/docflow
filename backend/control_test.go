package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestControlDeskIntegration(t *testing.T) {
	u := os.Getenv("DOCFLOW_TEST_DATABASE_URL")
	if u == "" {
		t.Skip("dedicated DB required")
	}
	cfg, e := pgxpool.ParseConfig(u)
	if e != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "docflow_test_") {
		t.Fatal("requires test DB")
	}
	ctx := context.Background()
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := &Server{db: db, secret: []byte(strings.Repeat("c", 48)), storage: t.TempDir()}
	if e = s.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	users := []string{}
	for i, role := range []string{"admin", "employee", "employee"} {
		var id string
		e = db.QueryRow(ctx, "INSERT INTO users(email,password_hash,full_name,role) VALUES($1,'unused',$1,$2) RETURNING id", []string{"control-admin@test.local", "control-owner@test.local", "control-other@test.local"}[i], role).Scan(&id)
		if e != nil {
			t.Fatal(e)
		}
		users = append(users, id)
	}
	h := s.routes()
	call := func(user int, method, path string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+s.makeAccess(users[user]))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var typ, doc string
	if e = db.QueryRow(ctx, "INSERT INTO document_types(code,name) VALUES('control_test','Control') RETURNING id").Scan(&typ); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(ctx, "INSERT INTO documents(type_id,title,author_id) VALUES($1,'Private control document',$2) RETURNING id", typ, users[1]).Scan(&doc); e != nil {
		t.Fatal(e)
	}
	input := map[string]any{"kind": "reminder", "title": "Check due", "body": "Private note", "document_id": doc, "collection": "Project", "due_at": time.Now().Add(time.Hour).Format(time.RFC3339), "version": 0}
	var created map[string]string
	_ = json.Unmarshal(call(1, "POST", "/desk/items", input, 200), &created)
	id := created["id"]
	if !bytes.Contains(call(1, "GET", "/desk", nil, 200), []byte("Private note")) || bytes.Contains(call(2, "GET", "/desk", nil, 200), []byte("Private note")) {
		t.Fatal("personal data isolation")
	}
	input["version"] = 1
	call(2, "PUT", "/desk/items/"+id, input, 404)
	call(2, "DELETE", "/desk/items/"+id, nil, 404)
	input["done"] = true
	call(1, "PUT", "/desk/items/"+id, input, 200)
	call(1, "PUT", "/desk/items/"+id, input, 409)
	call(1, "POST", "/documents/"+doc+"/seen", nil, 200)
	if !bytes.Contains(call(1, "GET", "/desk?part=recent", nil, 200), []byte("Private control document")) {
		t.Fatal("recent missing")
	}
	call(2, "POST", "/documents/"+doc+"/seen", nil, 404)
	if _, e = db.Exec(ctx, "UPDATE documents SET deleted_at=now() WHERE id=$1", doc); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(call(1, "GET", "/desk", nil, 200), []byte("Private control document")) || bytes.Contains(call(1, "GET", "/desk?part=recent", nil, 200), []byte("Private control document")) {
		t.Fatal("deleted document leaked")
	}
	bulletin := map[string]any{"title": "Admins only", "body": "Body", "severity": "urgent", "audience": "admin", "active": true, "pinned": true}
	call(1, "POST", "/admin/bulletins", bulletin, 403)
	var b map[string]string
	_ = json.Unmarshal(call(0, "POST", "/admin/bulletins", bulletin, 200), &b)
	bid := b["id"]
	if bytes.Contains(call(1, "GET", "/bulletins", nil, 200), []byte("Admins only")) {
		t.Fatal("wrong audience")
	}
	call(1, "POST", "/bulletins/"+bid+"/ack", nil, 200)
	var n int
	_ = db.QueryRow(ctx, "SELECT count(*) FROM bulletin_ack WHERE bulletin_id=$1", bid).Scan(&n)
	if n != 0 {
		t.Fatal("ack without access")
	}
	call(0, "POST", "/bulletins/"+bid+"/ack", nil, 200)
	call(0, "POST", "/bulletins/"+bid+"/ack", nil, 200)
	_ = db.QueryRow(ctx, "SELECT count(*) FROM bulletin_ack WHERE bulletin_id=$1", bid).Scan(&n)
	if n != 1 {
		t.Fatal("ack not idempotent")
	}
	bulletin["version"] = 1
	bulletin["audience"] = "all"
	bulletin["starts_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	call(0, "PUT", "/admin/bulletins/"+bid, bulletin, 200)
	if bytes.Contains(call(1, "GET", "/bulletins", nil, 200), []byte("Admins only")) {
		t.Fatal("scheduled bulletin leaked")
	}
	call(1, "GET", "/bulletins?all=true", nil, 403)
	for report := range controlReports {
		call(0, "GET", "/admin/control?report="+report, nil, 200)
		call(0, "GET", "/admin/control?report="+report+"&format=csv", nil, 200)
		call(1, "GET", "/admin/control?report="+report, nil, 403)
	}
	call(0, "GET", "/admin/control?report=sql-injection", nil, 400)
	call(1, "DELETE", "/desk/items/"+id, nil, 200)
}
