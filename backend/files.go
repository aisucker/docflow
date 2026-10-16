package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUpload = 20 << 20

func validUpload(name string, header []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf":
		if len(header) < 5 || string(header[:5]) != "%PDF-" {
			return ""
		}
		return "application/pdf"
	case ".docx":
		if len(header) < 4 || string(header[:4]) != "PK\x03\x04" {
			return ""
		}
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return ""
	}
}
func (s *Server) storeMultipart(w http.ResponseWriter, r *http.Request) (string, string, string, int64, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, 413, "Размер файла превышен или неверная форма")
		return "", "", "", 0, false
	}
	in, h, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "Отсутствует файл")
		return "", "", "", 0, false
	}
	defer in.Close()
	name := filepath.Base(strings.ReplaceAll(h.Filename, "\\", "/"))
	if len(name) > 180 || name == "." || name == "" {
		fail(w, 400, "Некорректное имя файла")
		return "", "", "", 0, false
	}
	head := make([]byte, 8)
	n, _ := io.ReadFull(in, head)
	kind := validUpload(name, head[:n])
	if kind == "" {
		fail(w, 400, "Допустимы только корректные PDF и DOCX")
		return "", "", "", 0, false
	}
	_, _ = in.Seek(0, 0)
	key := randomToken()
	f, err := os.OpenFile(safePath(s.storage, key), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fail(w, 500, "Не удалось сохранить файл")
		return "", "", "", 0, false
	}
	size, err := io.Copy(f, io.LimitReader(in, maxUpload+1))
	e2 := f.Close()
	if err != nil || e2 != nil || size > maxUpload {
		_ = os.Remove(safePath(s.storage, key))
		fail(w, 413, "Ошибка записи или файл слишком большой (20 МБ)")
		return "", "", "", 0, false
	}
	return name, kind, key, size, true
}
func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	rows, err := s.db.Query(r.Context(), "SELECT f.id,f.filename,f.mime_type,f.size,f.version,f.created_at,u.full_name FROM files f JOIN users u ON f.uploaded_by=u.id WHERE f.document_id=$1 ORDER BY f.filename,f.version DESC", id)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var fid, name, mimeType, user string
		var size int64
		var version int
		var created time.Time
		if e := rows.Scan(&fid, &name, &mimeType, &size, &version, &created, &user); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": fid, "filename": name, "mime_type": mimeType, "size": size, "version": version, "created_at": created, "uploaded_by": user})
	}
	respond(w, 200, out)
}
func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := me(r)
	if !s.readAllowed(r.Context(), id, p) {
		fail(w, 404, "Документ не найден")
		return
	}
	var author, st string
	if err := s.db.QueryRow(r.Context(), "SELECT author_id,status FROM documents WHERE id=$1", id).Scan(&author, &st); err != nil {
		errDB(w, err)
		return
	}
	if p.ID != author && p.Role != "admin" {
		fail(w, 403, "Добавлять файлы может автор или администратор")
		return
	}
	if st != "draft" && st != "returned" && st != "rejected" {
		fail(w, 409, "Нельзя изменять вложения на активном маршруте или после утверждения")
		return
	}
	name, mimeType, key, size, ok := s.storeMultipart(w, r)
	if !ok {
		return
	}
	defer func() {
		if key != "" {
			_ = os.Remove(safePath(s.storage, key))
		}
	}()
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var state string
	if err = tx.QueryRow(r.Context(), "SELECT status FROM documents WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&state); err != nil {
		errDB(w, err)
		return
	}
	if state != "draft" && state != "returned" && state != "rejected" {
		fail(w, 409, "Состояние документа изменилось")
		return
	}
	var version int
	err = tx.QueryRow(r.Context(), "SELECT coalesce(max(version),0)+1 FROM files WHERE document_id=$1 AND filename=$2", id, name).Scan(&version)
	if err != nil {
		errDB(w, err)
		return
	}
	var fileID string
	err = tx.QueryRow(r.Context(), "INSERT INTO files(document_id,filename,mime_type,storage_key,size,version,uploaded_by) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id", id, name, mimeType, key, size, version, p.ID).Scan(&fileID)
	if err != nil {
		errDB(w, err)
		return
	}
	if err = event(r.Context(), tx, id, p.ID, "file.uploaded", map[string]any{"file_id": fileID, "filename": name, "version": version}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	key = ""
	respond(w, 201, map[string]any{"id": fileID, "version": version})
}
func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var docID, name, kind, key string
	err := s.db.QueryRow(r.Context(), "SELECT document_id,filename,mime_type,storage_key FROM files WHERE id=$1", id).Scan(&docID, &name, &kind, &key)
	if err != nil {
		errDB(w, err)
		return
	}
	if !s.readAllowed(r.Context(), docID, me(r)) {
		fail(w, 404, "Файл не найден")
		return
	}
	name = strings.Map(func(c rune) rune {
		if c == '\n' || c == '\r' {
			return -1
		}
		return c
	}, name)
	w.Header().Set("Content-Type", kind)
	disposition := "attachment"
	if kind == "application/pdf" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeFile(w, r, safePath(s.storage, key))
}
func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), "SELECT id,name,coalesce(type_id::text,''),filename,created_at FROM templates ORDER BY name")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, typeID, file string
		var created time.Time
		if e := rows.Scan(&id, &name, &typeID, &file, &created); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "type_id": typeID, "filename": file, "created_at": created})
	}
	respond(w, 200, out)
}
func (s *Server) uploadTemplate(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	name, kind, key, _, ok := s.storeMultipart(w, r)
	if !ok {
		return
	}
	if kind != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		_ = os.Remove(safePath(s.storage, key))
		fail(w, 400, "Шаблон должен быть DOCX")
		return
	}
	title := strings.TrimSpace(r.FormValue("name"))
	if title == "" {
		title = name
	}
	var id string
	err := s.db.QueryRow(r.Context(), "INSERT INTO templates(name,type_id,filename,storage_key,created_by) VALUES($1,$2,$3,$4,$5) RETURNING id", title, nullableStr(r.FormValue("type_id")), name, key, me(r).ID).Scan(&id)
	if err != nil {
		_ = os.Remove(safePath(s.storage, key))
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func xmlEscape(v string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(v))
	return buf.String()
}
func renderDOCX(src, dst string, vals map[string]string) error {
	reader, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > 1000 {
		return errors.New("DOCX has too many parts")
	}
	total := int64(0)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	zipw := zip.NewWriter(f)
	for _, part := range reader.File {
		if len(part.Name) > 256 || part.UncompressedSize64 > 30<<20 {
			return errors.New("invalid DOCX")
		}
		total += int64(part.UncompressedSize64)
		if total > 80<<20 {
			return errors.New("DOCX exceeds expanded size limit")
		}
		out, err := zipw.CreateHeader(&zip.FileHeader{Name: part.Name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		in, err := part.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(in, 30<<20))
		_ = in.Close()
		if err != nil {
			return err
		}
		if strings.HasPrefix(part.Name, "word/") && strings.HasSuffix(part.Name, ".xml") {
			content := string(b)
			for k, v := range vals {
				content = strings.ReplaceAll(content, "{{"+k+"}}", xmlEscape(v))
			}
			b = []byte(content)
		}
		if _, err = out.Write(b); err != nil {
			return err
		}
	}
	if err = zipw.Close(); err != nil {
		return err
	}
	return f.Close()
}
func (s *Server) generateFile(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("id")
	p := me(r)
	if !s.readAllowed(r.Context(), docID, p) {
		fail(w, 404, "Документ не найден")
		return
	}
	var in struct {
		TemplateID string `json:"template_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	var title, registration, summary, authorID, status, docType string
	var fields []byte
	err := s.db.QueryRow(r.Context(), "SELECT title,coalesce(registration_number,''),summary,author_id,status,type_id,fields FROM documents WHERE id=$1", docID).Scan(&title, &registration, &summary, &authorID, &status, &docType, &fields)
	if err != nil {
		errDB(w, err)
		return
	}
	if p.ID != authorID && p.Role != "admin" {
		fail(w, 403, "Генерация доступна автору")
		return
	}
	if status != "draft" && status != "returned" && status != "rejected" {
		fail(w, 409, "Генерация доступна до согласования")
		return
	}
	var srcKey, templateName, templateType string
	err = s.db.QueryRow(r.Context(), "SELECT storage_key,name,coalesce(type_id::text,'') FROM templates WHERE id=$1", in.TemplateID).Scan(&srcKey, &templateName, &templateType)
	if err != nil {
		errDB(w, err)
		return
	}
	if templateType != "" && templateType != docType {
		fail(w, 400, "Шаблон не подходит к типу документа")
		return
	}
	vals := map[string]string{"title": title, "registration_number": registration, "summary": summary, "created_at": time.Now().Format("02.01.2006")}
	var dynamic map[string]any
	_ = json.Unmarshal(fields, &dynamic)
	for k, v := range dynamic {
		vals[k] = fmt.Sprint(v)
	}
	key := randomToken()
	if err = renderDOCX(safePath(s.storage, srcKey), safePath(s.storage, key), vals); err != nil {
		_ = os.Remove(safePath(s.storage, key))
		fail(w, 500, "Не удалось сформировать DOCX: проверьте шаблон")
		return
	}
	defer func() {
		if key != "" {
			_ = os.Remove(safePath(s.storage, key))
		}
	}()
	info, err := os.Stat(safePath(s.storage, key))
	if err != nil {
		fail(w, 500, "Ошибка файла")
		return
	}
	filename := strings.TrimSuffix(templateName, filepath.Ext(templateName)) + ".docx"
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var state string
	if err = tx.QueryRow(r.Context(), "SELECT status FROM documents WHERE id=$1 FOR UPDATE", docID).Scan(&state); err != nil {
		errDB(w, err)
		return
	}
	if state != "draft" && state != "returned" && state != "rejected" {
		fail(w, 409, "Документ перешёл на согласование")
		return
	}
	var version int
	err = tx.QueryRow(r.Context(), "SELECT coalesce(max(version),0)+1 FROM files WHERE document_id=$1 AND filename=$2", docID, filename).Scan(&version)
	if err != nil {
		errDB(w, err)
		return
	}
	var fileID string
	err = tx.QueryRow(r.Context(), "INSERT INTO files(document_id,filename,mime_type,storage_key,size,version,uploaded_by) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id", docID, filename, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", key, info.Size(), version, p.ID).Scan(&fileID)
	if err != nil {
		errDB(w, err)
		return
	}
	if err = event(r.Context(), tx, docID, p.ID, "file.generated", map[string]any{"file_id": fileID, "template_id": in.TemplateID}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	key = ""
	respond(w, 201, map[string]any{"id": fileID, "version": version})
}
