package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Server struct {
	db      *pgxpool.Pool
	secret  []byte
	storage string
	origin  string
}
type Principal struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"full_name"`
	Role  string `json:"role"`
}
type identityKey struct{}
type APIError struct {
	Error string `json:"error"`
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func main() {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL required")
	}
	secret := os.Getenv("APP_SECRET")
	if len(secret) < 32 {
		log.Fatal("APP_SECRET must be at least 32 characters")
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	var pingErr error
	for i := 0; i < 25; i++ {
		pingErr = pool.Ping(ctx)
		if pingErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if pingErr != nil {
		log.Fatal(pingErr)
	}
	storage := env("STORAGE_DIR", "./data")
	if err := os.MkdirAll(storage, 0700); err != nil {
		log.Fatal(err)
	}
	s := &Server{db: pool, secret: []byte(secret), storage: storage, origin: env("APP_ORIGIN", "http://localhost:8081")}
	if err := s.migrate(ctx); err != nil {
		log.Fatal("migrations: ", err)
	}
	if err := s.seed(ctx); err != nil {
		log.Fatal("bootstrap: ", err)
	}
	srv := &http.Server{Addr: env("APP_ADDR", ":8080"), Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	go s.webhookWorker(ctx)
	log.Printf("DocFlow API listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}
func (s *Server) migrate(ctx context.Context) error {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(981772181)"); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(981772181)")
	if _, err = conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}
		var done bool
		err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", f.Name()).Scan(&done)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		b, e := migrations.ReadFile("migrations/" + f.Name())
		if e != nil {
			return e
		}
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		for _, statement := range strings.Split(string(b), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, e = tx.Exec(ctx, statement); e != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("%s: %w", f.Name(), e)
			}
		}
		if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", f.Name()); e != nil {
			_ = tx.Rollback(ctx)
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) seed(ctx context.Context) error {
	defs := [][2]string{{"incoming", "Входящий документ"}, {"outgoing", "Исходящий документ"}, {"internal", "Внутренний документ"}, {"order", "Приказ"}, {"contract", "Договор"}}
	for _, d := range defs {
		if _, err := s.db.Exec(ctx, "INSERT INTO document_types(code,name) VALUES ($1,$2) ON CONFLICT (code) DO NOTHING", d[0], d[1]); err != nil {
			return err
		}
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin'").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	email, password := os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD")
	if email == "" || len(password) < 12 {
		return errors.New("ADMIN_EMAIL and ADMIN_PASSWORD (>=12 characters) required for initial bootstrap")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, "INSERT INTO users(email,password_hash,full_name,role) VALUES(lower($1),$2,$3,'admin')", email, string(hash), env("ADMIN_NAME", "Administrator"))
	return err
}
func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, APIError{Error: message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "Некорректный JSON: "+err.Error())
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, "Ожидается один JSON объект")
		return false
	}
	return true
}
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func tokenHash(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }
func me(r *http.Request) Principal  { v, _ := r.Context().Value(identityKey{}).(Principal); return v }
func mustRole(w http.ResponseWriter, p Principal, roles ...string) bool {
	for _, role := range roles {
		if p.Role == role {
			return true
		}
	}
	fail(w, 403, "Недостаточно прав")
	return false
}
func nullableStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return *v
}
func errDB(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "Не найдено")
		return
	}
	log.Printf("database error: %v", err)
	fail(w, 500, "Ошибка базы данных")
}
func goodID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func safePath(root, token string) string { return filepath.Join(root, filepath.Base(token)) }
func event(ctx context.Context, tx pgx.Tx, docID, actor, kind string, detail any) error {
	b, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, "INSERT INTO document_events(document_id,actor_id,kind,detail) VALUES($1,$2,$3,$4)", docID, nullableStr(actor), kind, b)
	return err
}
func emit(ctx context.Context, tx pgx.Tx, kind string, payload any) error {
	b, _ := json.Marshal(payload)
	_, err := tx.Exec(ctx, "INSERT INTO outbox(event_type,payload) VALUES($1,$2)", kind, b)
	return err
}
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]any{"status": "ok"}) })
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", s.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/me", s.authorize(s.profile))
	mux.HandleFunc("GET /api/v1/users", s.authorize(s.users))
	mux.HandleFunc("POST /api/v1/admin/users", s.authorize(s.createUser))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", s.authorize(s.updateUser))
	mux.HandleFunc("GET /api/v1/departments", s.authorize(s.departments))
	mux.HandleFunc("POST /api/v1/admin/departments", s.authorize(s.createDepartment))
	mux.HandleFunc("GET /api/v1/positions", s.authorize(s.positions))
	mux.HandleFunc("POST /api/v1/admin/positions", s.authorize(s.createPosition))
	mux.HandleFunc("GET /api/v1/document-types", s.authorize(s.documentTypes))
	mux.HandleFunc("POST /api/v1/admin/document-types", s.authorize(s.createDocumentType))
	mux.HandleFunc("PUT /api/v1/admin/document-types/{id}", s.authorize(s.updateDocumentType))
	mux.HandleFunc("GET /api/v1/documents", s.authorize(s.listDocuments))
	mux.HandleFunc("POST /api/v1/documents", s.authorize(s.createDocument))
	mux.HandleFunc("GET /api/v1/documents/{id}", s.authorize(s.getDocument))
	mux.HandleFunc("PATCH /api/v1/documents/{id}", s.authorize(s.updateDocument))
	mux.HandleFunc("DELETE /api/v1/documents/{id}", s.authorize(s.deleteDocument))
	mux.HandleFunc("GET /api/v1/admin/trash", s.authorize(s.trash))
	mux.HandleFunc("POST /api/v1/admin/documents/{id}/restore", s.authorize(s.restoreDocument))
	mux.HandleFunc("GET /api/v1/documents/{id}/history", s.authorize(s.documentHistory))
	mux.HandleFunc("GET /api/v1/documents/{id}/route", s.authorize(s.documentRoute))
	mux.HandleFunc("POST /api/v1/documents/{id}/start", s.authorize(s.startWorkflow))
	mux.HandleFunc("GET /api/v1/tasks", s.authorize(s.listTasks))
	mux.HandleFunc("POST /api/v1/tasks/{id}/decision", s.authorize(s.decideTask))
	mux.HandleFunc("GET /api/v1/workflows", s.authorize(s.listWorkflows))
	mux.HandleFunc("GET /api/v1/workflows/{id}", s.authorize(s.getWorkflow))
	mux.HandleFunc("POST /api/v1/admin/workflows", s.authorize(s.createWorkflow))
	mux.HandleFunc("PUT /api/v1/admin/workflows/{id}", s.authorize(s.updateWorkflow))
	mux.HandleFunc("GET /api/v1/documents/{id}/files", s.authorize(s.listFiles))
	mux.HandleFunc("POST /api/v1/documents/{id}/files", s.authorize(s.uploadFile))
	mux.HandleFunc("GET /api/v1/files/{id}/content", s.authorize(s.downloadFile))
	mux.HandleFunc("GET /api/v1/templates", s.authorize(s.listTemplates))
	mux.HandleFunc("POST /api/v1/admin/templates", s.authorize(s.uploadTemplate))
	mux.HandleFunc("POST /api/v1/documents/{id}/generate", s.authorize(s.generateFile))
	mux.HandleFunc("GET /api/v1/admin/webhooks", s.authorize(s.listWebhooks))
	mux.HandleFunc("POST /api/v1/admin/webhooks", s.authorize(s.createWebhook))
	mux.HandleFunc("POST /api/v1/admin/integration-tokens", s.authorize(s.createIntegrationToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Header.Get("Origin") != "" && r.Header.Get("Origin") == s.origin {
			w.Header().Set("Access-Control-Allow-Origin", s.origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
