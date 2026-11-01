package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOfficeProtocolIntegration(t *testing.T) {
	dbURL := os.Getenv("DOCFLOW_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("dedicated test DB not configured")
	}
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "docflow_test_") {
		t.Fatal("requires docflow_test_ database")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &Server{db: pool, secret: []byte(strings.Repeat("q", 48)), storage: t.TempDir()}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE office_settings SET builder_path='',docs_url='',internal_url='',storage_url='',jwt_secret=NULL WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	var admin string
	if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash,full_name,role) VALUES('office-protocol@test.local','unused','Office protocol','admin') RETURNING id").Scan(&admin); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(s.storage, strings.Repeat("b", 64))
	writeDOCXFixture(t, source, map[string]string{"word/document.xml": `<w:document xmlns:w="` + wordNamespace + `"><w:body><w:p><w:r><w:t>Saved by Docs</w:t></w:r></w:p></w:body></w:document>`})
	docx, _ := os.ReadFile(source)
	var template string
	if err = pool.QueryRow(ctx, "INSERT INTO templates(name,filename,storage_key,created_by) VALUES('Protocol','protocol.docx',$1,$2) RETURNING id", strings.Repeat("b", 64), admin).Scan(&template); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(s.routes())
	defer app.Close()
	secret := strings.Repeat("office-protocol-secret-", 2)
	var docs *httptest.Server
	requests := 0
	transfers := 0
	docs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthcheck":
			io.WriteString(w, "true")
		case "/cache/output.docx":
			w.Write(docx)
		case "/cache/output.pdf":
			w.Write([]byte("%PDF-1.4\nmock transport preview"))
		case "/docbuilder/from-file":
			requests++
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			in, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			data, _ := io.ReadAll(in)
			in.Close()
			script := string(data)
			if _, err = verifyOfficeJWT([]byte(secret), r.FormValue("token")); err != nil {
				t.Error("unsigned builder request", err)
			}
			if strings.Contains(script, "C:/") || strings.Contains(script, "E:/") || strings.Contains(script, "docjet-preview-") || strings.Contains(script, "docjet-generate-") {
				t.Error("filesystem path sent to Docs", script)
			}
			if start := strings.Index(script, "builder.OpenFile("); start >= 0 {
				literal := strings.Split(script[start+len("builder.OpenFile("):], ");")[0]
				var sourceURL string
				_ = json.Unmarshal([]byte(literal), &sourceURL)
				if !strings.HasPrefix(sourceURL, app.URL+"/api/v1/office/transfer/") {
					t.Error("unexpected source URL", sourceURL)
				} else {
					res, e := http.Get(sourceURL)
					if e != nil {
						t.Error(e)
					} else {
						if res.StatusCode != 200 {
							t.Error("transfer", res.StatusCode)
						}
						res.Body.Close()
						transfers++
					}
				}
			}
			filename := "output.docx"
			if strings.Contains(script, `"output.pdf"`) {
				filename = "output.pdf"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"end": true, "urls": map[string]string{filename: docs.URL + "/cache/" + filename}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer docs.Close()
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(method, app.URL+"/api/v1"+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+s.makeAccess(admin))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, want, data)
		}
		var value map[string]any
		_ = json.Unmarshal(data, &value)
		return value
	}
	call("PUT", "/admin/office", map[string]string{"docs_url": docs.URL, "internal_url": docs.URL, "storage_url": app.URL, "secret": secret}, 200)
	config := call("GET", "/templates/"+template+"/office?edit=true", nil, 200)["config"].(map[string]any)
	editor := config["editorConfig"].(map[string]any)
	callbackURL := editor["callbackUrl"].(string)
	key := config["document"].(map[string]any)["key"].(string)
	postCallback := func(token string, want int) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"token": token, "url": "http://untrusted.invalid/ignored.docx"})
		res, err := http.Post(callbackURL, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			data, _ := io.ReadAll(res.Body)
			t.Fatalf("callback: %d want %d %s", res.StatusCode, want, data)
		}
	}
	postCallback("invalid", 401)
	signed := officeJWT([]byte(secret), map[string]any{"payload": map[string]any{"status": 6, "key": key, "url": docs.URL + "/cache/output.docx"}})
	postCallback(signed, 200)
	postCallback(signed, 200)
	var version int
	if err = pool.QueryRow(ctx, "SELECT version FROM templates WHERE id=$1", template).Scan(&version); err != nil || version != 2 {
		t.Fatal("callback must save once", version, err)
	}
	postCallback(officeJWT([]byte(secret), map[string]any{"status": 2, "key": key, "url": "http://127.0.0.1:1/private"}), 422)
	call("GET", "/templates/"+template+"/preview", nil, 200)
	call("POST", "/admin/templates/create", map[string]any{"name": "Remote Builder", "blocks": []templateBlock{{Text: "Title: ", Tag: "title", Label: "Title"}}}, 201)
	if requests != 2 || transfers != 1 {
		t.Fatal("Builder API not exercised", requests, transfers)
	}
	// A browser config token cannot be used as a file-download capability.
	req := httptest.NewRequest("GET", "/api/v1/office/content/template/"+template+"?token="+config["token"].(string), nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("token scope confusion", rec.Code)
	}
}
