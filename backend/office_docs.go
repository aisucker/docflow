package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var officeHTTPClient = &http.Client{Timeout: 80 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return fmt.Errorf("redirect refused") }}

func officeJWT(secret []byte, payload any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	data, _ := json.Marshal(payload)
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func verifyOfficeJWT(secret []byte, token string) (map[string]json.RawMessage, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "HS256" {
		return nil, fmt.Errorf("invalid algorithm")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(raw, &claims) != nil {
		return nil, fmt.Errorf("invalid claims")
	}
	if exp, ok := claims["exp"]; ok {
		var n int64
		if json.Unmarshal(exp, &n) != nil || time.Now().Unix() >= n {
			return nil, fmt.Errorf("expired")
		}
	}
	return claims, nil
}

type officeSourceAccess struct {
	DocumentID string
	TemplateID string
}
type officeAccessKey struct{}
type officeTicket struct {
	DocumentID string `json:"document_id,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
	Scope      string `json:"scope"`
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	User       string `json:"user"`
	Version    int    `json:"version"`
	Exp        int64  `json:"exp"`
}

func (s *Server) officeTicket(scope, kind, id, user string, version int) string {
	return officeJWT(s.secret, officeTicket{Scope: scope, Kind: kind, ID: id, User: user, Version: version, Exp: time.Now().Add(8 * time.Hour).Unix()})
}
func (s *Server) readOfficeTicket(token, scope, kind, id string) (officeTicket, Principal, error) {
	var ticket officeTicket
	var p Principal
	claims, err := verifyOfficeJWT(s.secret, token)
	if err != nil {
		return ticket, p, err
	}
	raw, _ := json.Marshal(claims)
	if json.Unmarshal(raw, &ticket) != nil || ticket.Scope != scope || ticket.Kind != kind || ticket.ID != id || ticket.Exp <= time.Now().Unix() {
		return ticket, p, fmt.Errorf("wrong scope")
	}
	err = s.db.QueryRow(context.Background(), "SELECT id,role,full_name,email FROM users WHERE id=$1 AND active", ticket.User).Scan(&p.ID, &p.Role, &p.Name, &p.Email)
	return ticket, p, err
}
func (s *Server) officeContent(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	ticket, p, err := s.readOfficeTicket(r.URL.Query().Get("token"), "content", kind, id)
	if err != nil {
		fail(w, 401, "Недействительная ссылка OnlyOffice")
		return
	}
	var key, name, doc string
	var version int
	switch kind {
	case "file":
		err = s.db.QueryRow(r.Context(), "SELECT storage_key,filename,document_id,version FROM files WHERE id=$1", id).Scan(&key, &name, &doc, &version)
		if err == nil && !s.readAllowed(r.Context(), doc, p) {
			err = fmt.Errorf("denied")
		}
	case "template":
		err = s.db.QueryRow(r.Context(), "SELECT storage_key,filename,version FROM templates WHERE id=$1", id).Scan(&key, &name, &version)
	default:
		err = fmt.Errorf("invalid kind")
	}
	if err != nil || version != ticket.Version {
		fail(w, 404, "Файл изменился или недоступен")
		return
	}
	kindMIME := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if strings.EqualFold(filepath.Ext(name), ".pdf") {
		kindMIME = "application/pdf"
	}
	servePrivateFile(w, r, safePath(s.storage, key), kindMIME, name)
}
func (s *Server) fileOfficeConfig(w http.ResponseWriter, r *http.Request) {
	var doc, name string
	var version int
	if s.db.QueryRow(r.Context(), "SELECT document_id,filename,version FROM files WHERE id=$1", r.PathValue("id")).Scan(&doc, &name, &version) != nil || !s.readAllowed(r.Context(), doc, me(r)) {
		fail(w, 404, "Файл не найден")
		return
	}
	s.makeOfficeConfig(w, r, "file", r.PathValue("id"), name, version, false)
}
func (s *Server) templateOfficeConfig(w http.ResponseWriter, r *http.Request) {
	edit := r.URL.Query().Get("edit") == "true"
	if edit && !mustRole(w, me(r), "admin") {
		return
	}
	var name string
	var version int
	if s.db.QueryRow(r.Context(), "SELECT filename,version FROM templates WHERE id=$1", r.PathValue("id")).Scan(&name, &version) != nil {
		fail(w, 404, "Шаблон не найден")
		return
	}
	s.makeOfficeConfig(w, r, "template", r.PathValue("id"), name, version, edit)
}
func (s *Server) makeOfficeConfig(w http.ResponseWriter, r *http.Request, kind, id, name string, version int, edit bool) {
	settings, err := s.loadOfficeSettings(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	if settings.DocsURL == "" || settings.StorageURL == "" || !settings.SecretSet {
		fail(w, 503, "OnlyOffice Docs не настроен. Доступен локальный PDF-предпросмотр через Builder")
		return
	}
	key := digest([]byte(kind + ":" + id + ":" + strconv.Itoa(version)))
	contentURL := settings.StorageURL + "/api/v1/office/content/" + kind + "/" + id + "?token=" + url.QueryEscape(s.officeTicket("content", kind, id, me(r).ID, version))
	editor := map[string]any{"mode": "view", "lang": "ru", "user": map[string]string{"id": me(r).ID, "name": me(r).Name}, "customization": map[string]any{"autosave": true, "forcesave": true}}
	if edit {
		var sessionID string
		err = s.db.QueryRow(r.Context(), "INSERT INTO office_sessions(template_id,user_id,document_key,base_version) VALUES($1,$2,$3,$4) RETURNING id", id, me(r).ID, key, version).Scan(&sessionID)
		if err != nil {
			errDB(w, err)
			return
		}
		editor["mode"] = "edit"
		editor["callbackUrl"] = settings.StorageURL + "/api/v1/office/callback/" + sessionID + "?ticket=" + url.QueryEscape(s.officeTicket("callback", "session", sessionID, me(r).ID, version))
	}
	fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	docType := "word"
	if fileType == "pdf" {
		docType = "pdf"
	}
	config := map[string]any{"documentType": docType, "type": "desktop", "width": "100%", "height": "100%", "document": map[string]any{"fileType": fileType, "key": key, "title": name, "url": contentURL, "permissions": map[string]bool{"edit": edit, "download": true, "print": true, "review": false}}, "editorConfig": editor}
	config["token"] = officeJWT([]byte(settings.Secret), config)
	respond(w, 200, map[string]any{"script_url": settings.DocsURL + "/web-apps/apps/api/documents/api.js", "config": config})
}
func downloadOfficeResult(ctx context.Context, settings officeSettings, address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || u.User != nil {
		return nil, fmt.Errorf("invalid output URL")
	}
	internal, _ := url.Parse(settings.InternalURL)
	public, _ := url.Parse(settings.DocsURL)
	if internal == nil || public == nil || u.Host == "" || ((u.Scheme != internal.Scheme || u.Host != internal.Host) && (u.Scheme != public.Scheme || u.Host != public.Host)) {
		return nil, fmt.Errorf("OnlyOffice returned an untrusted output URL")
	}
	u.Scheme = internal.Scheme
	u.Host = internal.Host
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	res, err := officeHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("download status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxUpload+1))
	if err != nil || len(data) > maxUpload {
		return nil, fmt.Errorf("invalid output size")
	}
	return data, nil
}
func (s *Server) officeCallback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ticket, p, err := s.readOfficeTicket(r.URL.Query().Get("ticket"), "callback", "session", id)
	if err != nil || p.Role != "admin" {
		fail(w, 401, "Недействительная сессия редактирования")
		return
	}
	settings, err := s.loadOfficeSettings(r.Context())
	if err != nil || !settings.SecretSet {
		fail(w, 503, "OnlyOffice не настроен")
		return
	}
	var body map[string]json.RawMessage
	if !decode(w, r, &body) {
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw, ok := body["token"]; ok {
		_ = json.Unmarshal(raw, &token)
	}
	claims, err := verifyOfficeJWT([]byte(settings.Secret), token)
	if err != nil {
		fail(w, 401, "Неверная подпись OnlyOffice")
		return
	}
	if payload, ok := claims["payload"]; ok {
		if json.Unmarshal(payload, &claims) != nil {
			fail(w, 400, "Неверный callback")
			return
		}
	}
	var signed struct {
		Key    string `json:"key"`
		Status int    `json:"status"`
		URL    string `json:"url"`
	}
	raw, _ := json.Marshal(claims)
	if json.Unmarshal(raw, &signed) != nil || signed.Key == "" || signed.Status < 1 {
		fail(w, 400, "Callback не содержит подписанных данных")
		return
	}
	var templateID, key string
	var base int
	err = s.db.QueryRow(r.Context(), "SELECT template_id,document_key,base_version FROM office_sessions WHERE id=$1 AND user_id=$2 AND expires_at>now()", id, p.ID).Scan(&templateID, &key, &base)
	if err != nil || key != signed.Key || ticket.Version > base {
		fail(w, 409, "Сессия устарела")
		return
	}
	if signed.Status != 2 && signed.Status != 6 {
		respond(w, 200, map[string]int{"error": 0})
		return
	}
	data, err := downloadOfficeResult(r.Context(), settings, signed.URL)
	if err != nil {
		fail(w, 422, "Не удалось получить сохранённый шаблон из OnlyOffice")
		return
	}
	storageKey := randomToken()
	path := safePath(s.storage, storageKey)
	if err = os.WriteFile(path, data, 0600); err != nil {
		errDB(w, err)
		return
	}
	defer func() {
		if storageKey != "" {
			_ = os.Remove(path)
		}
	}()
	if _, _, err = templateControlTags(path); err != nil {
		fail(w, 422, "OnlyOffice вернул некорректный DOCX")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var currentVersion int
	var oldKey, lastHash string
	if err = tx.QueryRow(r.Context(), "SELECT t.version,t.storage_key,o.last_hash FROM templates t JOIN office_sessions o ON o.template_id=t.id WHERE o.id=$1 FOR UPDATE OF t,o", id).Scan(&currentVersion, &oldKey, &lastHash); err != nil {
		errDB(w, err)
		return
	}
	hash := digest(data)
	if hash == lastHash {
		respond(w, 200, map[string]int{"error": 0})
		return
	}
	if currentVersion != base {
		fail(w, 409, "Шаблон изменён в другой сессии. Сохранение отменено")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE templates SET storage_key=$1,version=version+1,updated_at=clock_timestamp() WHERE id=$2", storageKey, templateID); err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE office_sessions SET base_version=$1,last_hash=$2 WHERE id=$3", currentVersion+1, hash, id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		errDB(w, err)
		return
	}
	storageKey = ""
	_ = oldKey
	respond(w, 200, map[string]int{"error": 0})
}
func (s *Server) replaceTemplate(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	name, kind, key, _, ok := s.storeMultipart(w, r)
	if !ok {
		return
	}
	defer func() {
		if key != "" {
			_ = os.Remove(safePath(s.storage, key))
		}
	}()
	if !strings.HasSuffix(kind, "wordprocessingml.document") {
		fail(w, 400, "Нужен DOCX")
		return
	}
	if _, _, err := templateControlTags(safePath(s.storage, key)); err != nil {
		fail(w, 422, err.Error())
		return
	}
	version, err := strconv.Atoi(r.FormValue("version"))
	if err != nil || version < 1 {
		fail(w, 400, "Обновите версию шаблона")
		return
	}
	var newVersion int
	err = s.db.QueryRow(r.Context(), "UPDATE templates SET storage_key=$1,filename=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$3 AND version=$4 RETURNING version", key, name, r.PathValue("id"), version).Scan(&newVersion)
	if err == pgx.ErrNoRows {
		fail(w, 409, "Шаблон изменился. Обновите список")
		return
	}
	if err != nil {
		errDB(w, err)
		return
	}
	key = ""
	respond(w, 200, map[string]int{"version": newVersion})
}

// Docs v10 accepts scripts as multipart files. Uploaded document contents are
// temporary, authenticated transfers; no local filesystem paths are sent to Docs.
func (s *Server) runRemoteBuilder(ctx context.Context, settings officeSettings, script, output string) error {
	extension := filepath.Ext(output)
	if extension == "" {
		extension = ".docx"
	}
	filename := "output" + extension
	script = strings.ReplaceAll(script, jsString(filepath.ToSlash(output)), jsString(filename))
	// Generated scripts can only open the single server-selected source file.
	const prefix = "builder.OpenFile("
	if start := strings.Index(script, prefix); start >= 0 {
		end := strings.Index(script[start+len(prefix):], ");")
		if end < 0 {
			return fmt.Errorf("invalid script")
		}
		literal := script[start+len(prefix) : start+len(prefix)+end]
		var source string
		if json.Unmarshal([]byte(literal), &source) != nil {
			return fmt.Errorf("invalid source")
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if len(data) > maxUpload {
			return fmt.Errorf("source too large")
		}
		key := randomToken()
		if err = os.WriteFile(safePath(s.storage, key), data, 0600); err != nil {
			return err
		}
		defer os.Remove(safePath(s.storage, key))
		p, ok := ctx.Value(identityKey{}).(Principal)
		if !ok {
			return fmt.Errorf("missing identity")
		}
		access, _ := ctx.Value(officeAccessKey{}).(officeSourceAccess)
		token := officeJWT(s.secret, officeTicket{Scope: "transfer", Kind: "temporary", ID: key, User: p.ID, DocumentID: access.DocumentID, TemplateID: access.TemplateID, Exp: time.Now().Add(5 * time.Minute).Unix()})
		sourceURL := settings.StorageURL + "/api/v1/office/transfer/" + key + "?token=" + url.QueryEscape(token)
		script = strings.Replace(script, prefix+literal+");", prefix+jsString(sourceURL)+");", 1)
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, _ := writer.CreateFormFile("file", "build.js")
	_, _ = io.WriteString(part, script)
	params := map[string]any{"async": false}
	raw, _ := json.Marshal(params)
	_ = writer.WriteField("params", string(raw))
	token := officeJWT([]byte(settings.Secret), params)
	_ = writer.WriteField("token", token)
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", settings.InternalURL+"/docbuilder/from-file", &buffer)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := officeHTTPClient.Do(req)
	if err != nil {
		return workspaceError{503, "OnlyOffice Docs недоступен"}
	}
	defer res.Body.Close()
	var reply struct {
		End   bool              `json:"end"`
		Error int               `json:"error"`
		URLs  map[string]string `json:"urls"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&reply) != nil || !reply.End || reply.Error != 0 {
		return workspaceError{503, "OnlyOffice Docs не выполнил генерацию. Требуется Docs 10+ с включённым Document Builder API"}
	}
	address := reply.URLs[filename]
	if address == "" {
		return workspaceError{422, "OnlyOffice не вернул файл результата"}
	}
	data, err := downloadOfficeResult(ctx, settings, address)
	if err != nil {
		return workspaceError{422, "Не удалось скачать результат OnlyOffice"}
	}
	return os.WriteFile(output, data, 0600)
}
func (s *Server) officeTransfer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !receiptToken.MatchString(id) {
		fail(w, 404, "Не найдено")
		return
	}
	ticket, p, err := s.readOfficeTicket(r.URL.Query().Get("token"), "transfer", "temporary", id)
	if err != nil {
		fail(w, 401, "Недействительная ссылка")
		return
	}
	if ticket.DocumentID != "" {
		if !s.readAllowed(r.Context(), ticket.DocumentID, p) {
			fail(w, 404, "Документ недоступен")
			return
		}
	} else if ticket.TemplateID != "" {
		var exists bool
		if s.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM templates WHERE id=$1)", ticket.TemplateID).Scan(&exists) != nil || !exists {
			fail(w, 404, "Шаблон недоступен")
			return
		}
	} else if p.Role != "admin" {
		fail(w, 403, "Доступ запрещён")
		return
	}
	servePrivateFile(w, r, safePath(s.storage, id), "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "source.docx")
}
