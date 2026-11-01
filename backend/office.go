package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type officeSettings struct {
	BuilderPath string `json:"builder_path"`
	DocsURL     string `json:"docs_url"`
	InternalURL string `json:"internal_url"`
	StorageURL  string `json:"storage_url"`
	Secret      string `json:"-"`
	SecretSet   bool   `json:"secret_set"`
}

var officeSlots = make(chan struct{}, 2)

func (s *Server) officeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/office/status", s.authorize(s.officeStatus))
	mux.HandleFunc("GET /api/v1/admin/office", s.authorize(s.getOfficeSettings))
	mux.HandleFunc("PUT /api/v1/admin/office", s.authorize(s.saveOfficeSettings))
	mux.HandleFunc("GET /api/v1/files/{id}/preview", s.authorize(s.previewOfficeFile))
	mux.HandleFunc("GET /api/v1/templates/{id}/content", s.authorize(s.downloadTemplate))
	mux.HandleFunc("GET /api/v1/templates/{id}/controls", s.authorize(s.templateControls))
	mux.HandleFunc("GET /api/v1/templates/{id}/preview", s.authorize(s.previewTemplate))
	mux.HandleFunc("POST /api/v1/admin/templates/create", s.authorize(s.createOfficeTemplate))
	mux.HandleFunc("PUT /api/v1/admin/templates/{id}/file", s.authorize(s.replaceTemplate))
	mux.HandleFunc("GET /api/v1/files/{id}/office", s.authorize(s.fileOfficeConfig))
	mux.HandleFunc("GET /api/v1/templates/{id}/office", s.authorize(s.templateOfficeConfig))
	mux.HandleFunc("GET /api/v1/office/content/{kind}/{id}", s.officeContent)
	mux.HandleFunc("POST /api/v1/office/callback/{id}", s.officeCallback)
	mux.HandleFunc("GET /api/v1/office/transfer/{id}", s.officeTransfer)
}
func (s *Server) secretCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(append([]byte("docjet-onlyoffice:"), s.secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Server) encryptOfficeSecret(value string) ([]byte, error) {
	aead, err := s.secretCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(value), nil), nil
}
func (s *Server) loadOfficeSettings(ctx context.Context) (officeSettings, error) {
	var v officeSettings
	var encrypted []byte
	err := s.db.QueryRow(ctx, "SELECT builder_path,docs_url,internal_url,storage_url,jwt_secret FROM office_settings WHERE singleton").Scan(&v.BuilderPath, &v.DocsURL, &v.InternalURL, &v.StorageURL, &encrypted)
	if err != nil {
		return v, err
	}
	if len(encrypted) > 0 {
		aead, err := s.secretCipher()
		if err != nil || len(encrypted) < aead.NonceSize() {
			return v, fmt.Errorf("Невозможно прочитать секрет OnlyOffice")
		}
		secret, err := aead.Open(nil, encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():], nil)
		if err != nil {
			return v, fmt.Errorf("Невозможно прочитать секрет OnlyOffice")
		}
		v.Secret = string(secret)
	}
	if value := os.Getenv("ONLYOFFICE_BUILDER_PATH"); value != "" {
		v.BuilderPath = value
	}
	if value := os.Getenv("ONLYOFFICE_URL"); value != "" {
		v.DocsURL = value
	}
	if value := os.Getenv("ONLYOFFICE_INTERNAL_URL"); value != "" {
		v.InternalURL = value
	}
	if value := os.Getenv("ONLYOFFICE_STORAGE_URL"); value != "" {
		v.StorageURL = value
	}
	if value := os.Getenv("ONLYOFFICE_JWT_SECRET"); value != "" {
		v.Secret = value
	}
	if v.InternalURL == "" {
		v.InternalURL = v.DocsURL
	}
	v.SecretSet = v.Secret != ""
	return v, nil
}
func officeURL(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == ""
}
func builderAvailable(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	name := strings.ToLower(filepath.Base(path))
	if name != "docbuilder.exe" && name != "docbuilder" && name != "documentbuilder" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
func (s *Server) getOfficeSettings(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	v, err := s.loadOfficeSettings(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, v)
}
func (s *Server) saveOfficeSettings(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		BuilderPath string `json:"builder_path"`
		DocsURL     string `json:"docs_url"`
		InternalURL string `json:"internal_url"`
		StorageURL  string `json:"storage_url"`
		Secret      string `json:"secret"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.BuilderPath = strings.TrimSpace(in.BuilderPath)
	in.DocsURL = strings.TrimRight(strings.TrimSpace(in.DocsURL), "/")
	in.InternalURL = strings.TrimRight(strings.TrimSpace(in.InternalURL), "/")
	in.StorageURL = strings.TrimRight(strings.TrimSpace(in.StorageURL), "/")
	if in.BuilderPath != "" && !builderAvailable(in.BuilderPath) {
		fail(w, 400, "Укажите полный путь к установленному docbuilder.exe / documentbuilder")
		return
	}
	if !officeURL(in.DocsURL) || !officeURL(in.InternalURL) || !officeURL(in.StorageURL) {
		fail(w, 400, "Адреса OnlyOffice должны иметь вид http(s)://host:port без пути")
		return
	}
	var encrypted []byte
	var err error
	if in.Secret != "" {
		if len(in.Secret) < 32 || len(in.Secret) > 512 {
			fail(w, 400, "JWT-секрет: 32–512 символов")
			return
		}
		encrypted, err = s.encryptOfficeSecret(in.Secret)
		if err != nil {
			errDB(w, err)
			return
		}
	}
	current, err := s.loadOfficeSettings(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	if in.DocsURL != "" && (in.StorageURL == "" || (in.Secret == "" && !current.SecretSet)) {
		fail(w, 400, "Для Docs укажите адрес DocJet для сервера и JWT-секрет")
		return
	}
	_, err = s.db.Exec(r.Context(), "UPDATE office_settings SET builder_path=$1,docs_url=$2,internal_url=$3,storage_url=$4,jwt_secret=coalesce($5,jwt_secret),updated_at=now() WHERE singleton", in.BuilderPath, in.DocsURL, in.InternalURL, in.StorageURL, encrypted)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) officeStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadOfficeSettings(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	available := false
	if v.DocsURL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", v.InternalURL+"/healthcheck", nil)
		res, err := officeHTTPClient.Do(req)
		if err == nil {
			res.Body.Close()
			available = res.StatusCode == 200
		}
	}
	respond(w, 200, map[string]any{"builder_available": builderAvailable(v.BuilderPath), "docs_configured": v.DocsURL != "" && v.SecretSet, "docs_available": available, "builder_notice": "Бесплатный ONLYOFFICE Document Builder добавляет водяной знак. Для документов без него нужна соответствующая лицензия OnlyOffice.", "docs_url": v.DocsURL})
}
func (s *Server) runOfficeBuilder(ctx context.Context, settings officeSettings, script string, output string) error {

	select {
	case officeSlots <- struct{}{}:
		defer func() { <-officeSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	if !builderAvailable(settings.BuilderPath) {
		if settings.DocsURL != "" && settings.SecretSet {
			return s.runRemoteBuilder(ctx, settings, script, output)
		}
		return workspaceError{503, "OnlyOffice Builder не настроен. Откройте Администрирование → OnlyOffice"}
	}
	dir, err := os.MkdirTemp("", "docjet-builder-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "build.js")
	if err = os.WriteFile(file, []byte(script), 0600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, settings.BuilderPath, file)
	cmd.Dir = filepath.Dir(settings.BuilderPath)
	hideOfficeProcess(cmd)
	var log limitedOutput
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err = cmd.Run(); err != nil {
		return workspaceError{503, "OnlyOffice не смог обработать файл. Проверьте установку, лицензию и формат шаблона"}
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 || info.Size() > maxUpload {
		return workspaceError{422, "OnlyOffice не создал корректный файл до 20 МБ"}
	}
	return nil
}
func jsString(value string) string { data, _ := json.Marshal(value); return string(data) }
func (s *Server) convertOffice(ctx context.Context, source, target, format string) error {
	settings, err := s.loadOfficeSettings(ctx)
	if err != nil {
		return err
	}
	script := "builder.OpenFile(" + jsString(filepath.ToSlash(source)) + ");\nbuilder.SaveFile(" + jsString(format) + "," + jsString(filepath.ToSlash(target)) + ");\nbuilder.CloseFile();\n"
	return s.runOfficeBuilder(ctx, settings, script, target)
}
func servePrivateFile(w http.ResponseWriter, r *http.Request, path, kind, name string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeFile(w, r, path)
}
func (s *Server) previewOfficeFile(w http.ResponseWriter, r *http.Request) {
	var doc, key, name, kind string
	if s.db.QueryRow(r.Context(), "SELECT document_id,storage_key,filename,mime_type FROM files WHERE id=$1", r.PathValue("id")).Scan(&doc, &key, &name, &kind) != nil || !s.readAllowed(r.Context(), doc, me(r)) {
		fail(w, 404, "Файл не найден")
		return
	}
	source := safePath(s.storage, key)
	if kind == "application/pdf" {
		servePrivateFile(w, r, source, kind, name)
		return
	}
	s.serveOfficePreview(w, r.WithContext(context.WithValue(r.Context(), officeAccessKey{}, officeSourceAccess{DocumentID: doc})), source, name)
}
func (s *Server) serveOfficePreview(w http.ResponseWriter, r *http.Request, source, name string) {
	if _, _, err := templateControlTags(source); err != nil {
		fail(w, 422, "Некорректный DOCX: "+err.Error())
		return
	}
	dir, err := os.MkdirTemp("", "docjet-preview-")
	if err != nil {
		errDB(w, err)
		return
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "preview.pdf")
	if err = s.convertOffice(r.Context(), source, target, "pdf"); err != nil {
		workspaceFail(w, err)
		return
	}
	head, readErr := os.ReadFile(target)
	if readErr != nil || len(head) < 5 || string(head[:5]) != "%PDF-" {
		fail(w, 422, "OnlyOffice не создал корректный PDF")
		return
	}
	servePrivateFile(w, r, target, "application/pdf", strings.TrimSuffix(name, filepath.Ext(name))+".pdf")
}
func (s *Server) downloadTemplate(w http.ResponseWriter, r *http.Request) {
	var name, key string
	if s.db.QueryRow(r.Context(), "SELECT filename,storage_key FROM templates WHERE id=$1", r.PathValue("id")).Scan(&name, &key) != nil {
		fail(w, 404, "Шаблон не найден")
		return
	}
	servePrivateFile(w, r, safePath(s.storage, key), "application/vnd.openxmlformats-officedocument.wordprocessingml.document", name)
}
func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var name, key string
	if s.db.QueryRow(r.Context(), "SELECT filename,storage_key FROM templates WHERE id=$1", r.PathValue("id")).Scan(&name, &key) != nil {
		fail(w, 404, "Шаблон не найден")
		return
	}
	s.serveOfficePreview(w, r.WithContext(context.WithValue(r.Context(), officeAccessKey{}, officeSourceAccess{TemplateID: r.PathValue("id")})), safePath(s.storage, key), name)
}
func (s *Server) templateControls(w http.ResponseWriter, r *http.Request) {
	var key string
	if s.db.QueryRow(r.Context(), "SELECT storage_key FROM templates WHERE id=$1", r.PathValue("id")).Scan(&key) != nil {
		fail(w, 404, "Шаблон не найден")
		return
	}
	_, items, err := templateControlTags(safePath(s.storage, key))
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	respond(w, 200, items)
}

type templateBlock struct {
	Text  string `json:"text"`
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

func templateBuilderScript(blocks []templateBlock, target string) (string, error) {
	if len(blocks) == 0 || len(blocks) > 200 {
		return "", badWorkspace("Добавьте 1–200 блоков шаблона")
	}
	var out strings.Builder
	out.WriteString("builder.CreateFile(\"docx\");\nvar doc=Api.GetDocument();\n")
	for i, block := range blocks {
		if len(block.Text) > 10000 || len(block.Label) > 300 {
			return "", badWorkspace("Слишком длинный текст блока")
		}
		if block.Tag != "" && !tagCodePattern.MatchString(block.Tag) {
			return "", badWorkspace("Некорректный Tag")
		}
		if i == 0 {
			out.WriteString("var p=doc.GetElement(0);\n")
		} else {
			out.WriteString("var p=Api.CreateParagraph();doc.Push(p);\n")
		}
		if block.Text != "" {
			out.WriteString("p.AddText(" + jsString(block.Text) + ");\n")
		}
		if block.Tag != "" {
			out.WriteString("var c=Api.CreateInlineLvlSdt();c.SetTag(" + jsString(block.Tag) + ");c.SetAlias(" + jsString(block.Label) + ");c.AddText(" + jsString("«"+block.Tag+"»") + ");p.AddInlineLvlSdt(c);\n")
		}
	}
	out.WriteString("builder.SaveFile(\"docx\"," + jsString(filepath.ToSlash(target)) + ");\nbuilder.CloseFile();\n")
	return out.String(), nil
}
func (s *Server) createOfficeTemplate(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		Name   string          `json:"name"`
		TypeID string          `json:"type_id"`
		Blocks []templateBlock `json:"blocks"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 150 || (in.TypeID != "" && !goodID(in.TypeID)) {
		fail(w, 400, "Укажите название и тип шаблона")
		return
	}
	if in.TypeID != "" {
		var yes bool
		if s.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM document_types WHERE id=$1)", in.TypeID).Scan(&yes) != nil || !yes {
			fail(w, 400, "Тип документа не найден")
			return
		}
	}
	key := randomToken()
	path := safePath(s.storage, key)
	defer func() {
		if key != "" {
			_ = os.Remove(path)
		}
	}()
	script, err := templateBuilderScript(in.Blocks, path)
	if err != nil {
		workspaceFail(w, err)
		return
	}
	settings, err := s.loadOfficeSettings(r.Context())
	if err == nil {
		err = s.runOfficeBuilder(r.Context(), settings, script, path)
	}
	if err != nil {
		workspaceFail(w, err)
		return
	}
	if _, _, err = templateControlTags(path); err != nil {
		fail(w, 422, err.Error())
		return
	}
	var id string
	err = s.db.QueryRow(r.Context(), "INSERT INTO templates(name,type_id,filename,storage_key,created_by) VALUES($1,$2,$3,$4,$5) RETURNING id", in.Name, nullableStr(in.TypeID), in.Name+".docx", key, me(r).ID).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	key = ""
	respond(w, 201, map[string]string{"id": id})
}
