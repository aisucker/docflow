package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPlaceholderWorkerProcess(t *testing.T) {
	if os.Getenv("DOCJET_PLACEHOLDER_WORKER") != "1" {
		return
	}
	placeholderWorkerMain()
	os.Exit(0)
}
func TestPlaceholderIsolation(t *testing.T) {
	ctx := context.Background()
	document := json.RawMessage(`{"title":"Договор","due_at":"2026-11-01T20:00:00Z","fields":{"amount":12.5}}`)
	value, err := evaluatePlaceholder(ctx, `return document.title+" / "+helpers.date(document.due_at)+" / "+(document.fields.amount*2);`, document)
	if err != nil || value != "Договор / 02.11.2026 / 25" {
		t.Fatalf("%q %v", value, err)
	}
	for _, script := range []string{`while(true){}`, `return require('fs').readFileSync('x');`, `document.title='changed';return document.title;`, `return new Promise(()=>{});`, `return 'x'.repeat(100000);`} {
		if value, err = evaluatePlaceholder(ctx, script, document); err == nil {
			t.Fatalf("accepted script %s -> %q", script, value)
		}
	}
	value, err = evaluatePlaceholder(ctx, `return [typeof process,typeof require,typeof fetch,typeof console].join(',');`, document)
	if err != nil || value != "undefined,undefined,undefined,undefined" {
		t.Fatalf("globals: %q %v", value, err)
	}
	if err = validatePlaceholder("tag", "name", "return ("); err == nil {
		t.Fatal("invalid syntax")
	}
}
func writeDOCXFixture(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, data := range parts {
		out, e := writer.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = out.Write([]byte(data))
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}
func readDOCXPart(t *testing.T, path, part string) string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, f := range reader.File {
		if f.Name == part {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			data, _ := io.ReadAll(r)
			return string(data)
		}
	}
	t.Fatal("missing part", part)
	return ""
}
func TestContentControlRendering(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "template.docx"), filepath.Join(dir, "filled.docx")
	xml := `<w:document xmlns:w="` + wordNamespace + `"><w:body><w:p><w:r><w:t>До {{title}}</w:t></w:r><w:sdt><w:sdtPr><w:tag w:val="title"/><w:alias w:val="Название"/><w:id w:val="42"/><w:showingPlcHdr/></w:sdtPr><w:sdtContent><w:r><w:rPr><w:b/></w:rPr><w:t>СТАРОЕ</w:t></w:r></w:sdtContent></w:sdt></w:p><w:sdt><w:sdtPr><w:tag w:val="summary"/></w:sdtPr><w:sdtContent><w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:t>Старое содержание</w:t></w:r></w:p></w:sdtContent></w:sdt><w:p><w:r><w:t>После</w:t></w:r></w:p></w:body></w:document>`
	header := `<w:hdr xmlns:w="` + wordNamespace + `"><w:p><w:sdt><w:sdtPr><w:tag w:val="author_name"/></w:sdtPr><w:sdtContent><w:r><w:t>Автор</w:t></w:r></w:sdtContent></w:sdt></w:p></w:hdr>`
	writeDOCXFixture(t, src, map[string]string{"word/document.xml": xml, "word/header1.xml": header, "unchanged.bin": "original"})
	infos, err := processTemplate(src, dst, map[string]string{"title": "<Тест>& {{summary}}", "summary": "Строка 1\nСтрока 2\tтаб", "author_name": "Анна"}, map[string]string{"title": "Legacy", "summary": "MUST NOT REPLACE"})
	if err != nil || len(infos) != 3 {
		t.Fatalf("%v %v", infos, err)
	}
	result := readDOCXPart(t, dst, "word/document.xml")
	doc := etree.NewDocument()
	if err = doc.ReadFromString(result); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`w:val="title"`, `w:val="42"`, `w:b`, `w:jc`, `w:br`, `w:tab`, `&lt;Тест&gt;&amp; {{summary}}`, `До Legacy`, `После`} {
		if !strings.Contains(result, text) {
			t.Fatalf("missing %s in %s", text, result)
		}
	}
	if strings.Contains(result, "СТАРОЕ") || strings.Contains(result, "MUST NOT REPLACE") || strings.Contains(result, "showingPlcHdr") {
		t.Fatal(result)
	}
	if !strings.Contains(readDOCXPart(t, dst, "word/header1.xml"), "Анна") {
		t.Fatal("header not filled")
	}
	if readDOCXPart(t, dst, "unchanged.bin") != "original" {
		t.Fatal("unrelated part modified")
	}
	src2 := filepath.Join(dir, "nested.docx")
	writeDOCXFixture(t, src2, map[string]string{"word/document.xml": strings.Replace(xml, `<w:t>СТАРОЕ</w:t>`, `<w:sdt><w:sdtPr/><w:sdtContent/></w:sdt>`, 1)})
	if _, err = processTemplate(src2, filepath.Join(dir, "bad.docx"), map[string]string{"title": "x"}, nil); err == nil {
		t.Fatal("nested control silently destroyed")
	}
}

func TestOfficePlaceholdersIntegration(t *testing.T) {
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
	s := &Server{db: pool, secret: []byte(strings.Repeat("p", 48)), storage: t.TempDir()}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE office_settings SET builder_path='',docs_url='',internal_url='',storage_url='',jwt_secret=NULL WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	users := []string{}
	for i, role := range []string{"admin", "employee", "employee"} {
		var uid string
		err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash,full_name,role) VALUES($1,'unused',$1,$2) RETURNING id", []string{"placeholders-admin@test.local", "placeholders-author@test.local", "placeholders-outsider@test.local"}[i], role).Scan(&uid)
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, uid)
	}
	var typ string
	if err = pool.QueryRow(ctx, `INSERT INTO document_types(code,name,fields) VALUES('placeholder_test','Placeholder Test','[{"key":"amount","label":"Сумма","type":"number"}]') RETURNING id`).Scan(&typ); err != nil {
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
		var result map[string]any
		if json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Body.String())
		}
		return result
	}
	doc := obj(call(1, "POST", "/documents", documentInput{TypeID: typ, Title: "Договор & <тест>", Summary: "Поставка оборудования", Fields: map[string]any{"amount": 123.45}}, 201))["id"].(string)
	tagInput := map[string]any{"code": "amount_total", "name": "Сумма с надбавкой", "type_id": typ, "script": "return (document.fields.amount * 1.12).toFixed(2);", "active": true}
	call(1, "POST", "/admin/placeholder-tags", tagInput, 403)
	tag := obj(call(0, "POST", "/admin/placeholder-tags", tagInput, 201))
	tagID := tag["id"].(string)
	test := obj(call(0, "POST", "/admin/placeholder-tags/test", map[string]string{"document_id": doc, "script": tagInput["script"].(string)}, 200))
	if test["value"] != "138.26" {
		t.Fatal(test)
	}
	tagInput["version"] = 0
	call(0, "PUT", "/admin/placeholder-tags/"+tagID, tagInput, 400)
	tagInput["version"] = 1
	call(0, "PUT", "/admin/placeholder-tags/"+tagID, tagInput, 200)
	call(0, "PUT", "/admin/placeholder-tags/"+tagID, tagInput, 409)
	call(2, "GET", "/documents/"+doc+"/placeholder-values", nil, 404)
	value := obj(call(0, "POST", "/admin/placeholder-tags/test", map[string]string{"document_id": doc, "script": "return document.author.full_name;"}, 200))["value"]
	if value != "placeholders-author@test.local" {
		t.Fatal(value)
	}
	// A more specific Tag overrides the shared definition, without affecting other types.
	call(0, "POST", "/admin/placeholder-tags", map[string]any{"code": "title", "name": "Специальный заголовок", "type_id": typ, "script": "return 'СПЕЦ: ' + document.title;"}, 201)
	resolved, _, err := s.placeholderValues(ctx, doc, Principal{ID: users[1], Role: "employee"}, []string{"title", "amount_total"})
	if err != nil || !strings.HasPrefix(resolved["title"], "СПЕЦ: ") {
		t.Fatal(resolved, err)
	}
	// Secrets are encrypted at rest, masked on reads and required for web Docs.
	secret := strings.Repeat("OnlyOffice-secret-", 3)
	call(0, "PUT", "/admin/office", map[string]string{"docs_url": "http://localhost:8085", "internal_url": "http://localhost:8085", "storage_url": "http://localhost:8080", "secret": secret}, 200)
	settings := call(0, "GET", "/admin/office", nil, 200).Body.String()
	if strings.Contains(settings, secret) || !strings.Contains(settings, `"secret_set":true`) {
		t.Fatal(settings)
	}
	var encrypted []byte
	_ = pool.QueryRow(ctx, "SELECT jwt_secret FROM office_settings WHERE singleton").Scan(&encrypted)
	if bytes.Contains(encrypted, []byte(secret)) {
		t.Fatal("secret stored in plaintext")
	}
	call(1, "GET", "/admin/office", nil, 403)
	// Signed file capabilities are scoped to a resource and checked against current ACL.
	inputPath := filepath.Join(s.storage, strings.Repeat("a", 64))
	writeDOCXFixture(t, inputPath, map[string]string{"word/document.xml": `<w:document xmlns:w="` + wordNamespace + `"><w:body/></w:document>`})
	var fid string
	err = pool.QueryRow(ctx, "INSERT INTO files(document_id,filename,mime_type,storage_key,size,version,uploaded_by) VALUES($1,'test.docx','application/vnd.openxmlformats-officedocument.wordprocessingml.document',$2,20,1,$3) RETURNING id", doc, strings.Repeat("a", 64), users[1]).Scan(&fid)
	if err != nil {
		t.Fatal(err)
	}
	call(2, "GET", "/files/"+fid+"/office", nil, 404)
	config := obj(call(1, "GET", "/files/"+fid+"/office", nil, 200))
	if config["config"].(map[string]any)["document"].(map[string]any)["permissions"].(map[string]any)["edit"] != false {
		t.Fatal("attachment allowed editing")
	}
	ticket := s.officeTicket("content", "file", fid, users[1], 1)
	r := httptest.NewRequest("GET", "/api/v1/office/content/file/"+fid+"?token="+ticket, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := pool.Exec(ctx, "UPDATE documents SET deleted_at=now() WHERE id=$1", doc); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("deleted doc remained accessible", w.Code)
	}
	_, _ = pool.Exec(ctx, "UPDATE documents SET deleted_at=NULL WHERE id=$1", doc)
	// A real OnlyOffice Builder round trip checks SDT creation, filling and PDF preview.
	builder := os.Getenv("DOCJET_TEST_BUILDER_PATH")
	if !builderAvailable(builder) {
		t.Log("native builder test skipped: DOCJET_TEST_BUILDER_PATH absent")
		return
	}
	call(0, "PUT", "/admin/office", map[string]string{"builder_path": builder}, 200)
	template := obj(call(0, "POST", "/admin/templates/create", map[string]any{"name": "Договор для теста", "type_id": typ, "blocks": []templateBlock{{Text: "Документ: ", Tag: "title", Label: "Название"}, {Text: "Сумма: ", Tag: "amount_total", Label: "Сумма"}}}, 201))["id"].(string)
	call(1, "GET", "/templates/"+template+"/office?edit=true", nil, 403)
	generated := obj(call(1, "POST", "/documents/"+doc+"/generate", map[string]string{"template_id": template}, 201))["id"].(string)
	var key string
	_ = pool.QueryRow(ctx, "SELECT storage_key FROM files WHERE id=$1", generated).Scan(&key)
	content := readDOCXPart(t, safePath(s.storage, key), "word/document.xml")
	if !strings.Contains(content, "138.26") || !strings.Contains(content, "СПЕЦ:") || !strings.Contains(content, `w:val="amount_total"`) {
		t.Fatal("generated SDT values/tags missing", content)
	}
	preview := call(1, "GET", "/files/"+generated+"/preview", nil, 200)
	if !bytes.HasPrefix(preview.Body.Bytes(), []byte("%PDF-")) {
		t.Fatal("preview is not PDF")
	}
	call(2, "GET", "/files/"+generated+"/preview", nil, 404)
	_, _ = pool.Exec(ctx, "UPDATE documents SET status='approved' WHERE id=$1", doc)
	call(1, "POST", "/documents/"+doc+"/generate", map[string]string{"template_id": template}, 409)
	// Expired / wrong-algorithm JWTs are rejected.
	if _, err = verifyOfficeJWT([]byte(secret), officeJWT([]byte(secret), map[string]any{"exp": time.Now().Add(-time.Second).Unix()})); err == nil {
		t.Fatal("expired JWT")
	}
}

func TestDOCXRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../outside.txt", "/absolute.txt", "word/../../outside.txt", "C:/outside.txt"} {
		source := filepath.Join(dir, "unsafe.docx")
		writeDOCXFixture(t, source, map[string]string{"word/document.xml": `<w:document xmlns:w="` + wordNamespace + `"><w:body/></w:document>`, name: "unsafe"})
		if _, _, err := templateControlTags(source); err == nil {
			t.Fatalf("accepted ZIP path %s", name)
		}
	}
}
