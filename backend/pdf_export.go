package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

//go:embed pdf/render.py
var pdfRenderer []byte

//go:embed pdf/docx-to-pdf.ps1
var wordConverter []byte
var pdfSlots = make(chan struct{}, 2)
var verifySlots = make(chan struct{}, 2)
var receiptToken = regexp.MustCompile(`^[a-f0-9]{64}$`)

type pdfSignature struct {
	ID          string    `json:"id"`
	Name        string    `json:"signer_name"`
	Serial      string    `json:"certificate_serial"`
	PayloadHash string    `json:"payload_sha256"`
	VerifiedAt  time.Time `json:"verified_at"`
}
type pdfMetadata struct {
	Title        string         `json:"title"`
	Summary      string         `json:"summary,omitempty"`
	Fields       map[string]any `json:"fields,omitempty"`
	Filename     string         `json:"filename"`
	Project      string         `json:"project_number"`
	Registration string         `json:"registration_number"`
	RegisteredAt *time.Time     `json:"registered_at"`
	ExportedAt   time.Time      `json:"exported_at"`
	SourceHash   string         `json:"source_sha256"`
	VerifyURL    string         `json:"verify_url"`
	Signatures   []pdfSignature `json:"signatures"`
}
type signedManifest struct {
	Document struct {
		Title   string         `json:"title"`
		Summary string         `json:"summary"`
		Fields  map[string]any `json:"fields"`
	} `json:"document"`
	Files []struct {
		ID   string `json:"id"`
		Hash string `json:"sha256"`
	} `json:"files"`
}

func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func coversFile(payload []byte, id, hash string) bool {
	var m signedManifest
	if json.Unmarshal(payload, &m) != nil {
		return false
	}
	for _, f := range m.Files {
		if f.ID == id && f.Hash == hash {
			return true
		}
	}
	return false
}
func (s *Server) exportPDF(w http.ResponseWriter, r *http.Request) {
	select {
	case pdfSlots <- struct{}{}:
		defer func() { <-pdfSlots }()
	default:
		fail(w, 429, "Выгрузка занята, повторите через несколько секунд")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	id := r.PathValue("id")
	docID := id
	fileID := ""
	source := ""
	kind := ""
	m := pdfMetadata{Filename: "Карточка документа", ExportedAt: time.Now().UTC()}
	if strings.Contains(r.URL.Path, "/files/") {
		fileID = id
		var key string
		if s.db.QueryRow(ctx, "SELECT document_id,filename,mime_type,storage_key FROM files WHERE id=$1", id).Scan(&docID, &m.Filename, &kind, &key) != nil {
			fail(w, 404, "Файл не найден")
			return
		}
		source = safePath(s.storage, key)
	}
	if !s.readAllowed(ctx, docID, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	if err := s.db.QueryRow(ctx, "SELECT coalesce(project_number,''),coalesce(registration_number,''),registered_at FROM documents WHERE id=$1 AND deleted_at IS NULL", docID).Scan(&m.Project, &m.Registration, &m.RegisteredAt); err != nil {
		errDB(w, err)
		return
	}
	if source != "" {
		data, err := os.ReadFile(source)
		if err != nil {
			fail(w, 500, "Не удалось прочитать исходный файл")
			return
		}
		m.SourceHash = digest(data)
	}
	rows, err := s.db.Query(ctx, "SELECT id,signer_name,certificate_serial,payload_sha256,verified_at,payload FROM document_signatures WHERE document_id=$1 ORDER BY verified_at", docID)
	if err != nil {
		errDB(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var sig pdfSignature
		var payload []byte
		if err = rows.Scan(&sig.ID, &sig.Name, &sig.Serial, &sig.PayloadHash, &sig.VerifiedAt, &payload); err != nil {
			break
		}
		if fileID != "" && !coversFile(payload, fileID, m.SourceHash) {
			continue
		}
		var manifest signedManifest
		if json.Unmarshal(payload, &manifest) != nil || digest(payload) != sig.PayloadHash {
			err = fmt.Errorf("invalid stored signing manifest")
			break
		}
		m.Title = manifest.Document.Title
		m.Summary = manifest.Document.Summary
		m.Fields = manifest.Document.Fields
		m.Signatures = append(m.Signatures, sig)
		ids = append(ids, sig.ID)
		if fileID == "" {
			m.SourceHash = digest(payload)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		errDB(w, err)
		return
	}
	if len(m.Signatures) == 0 {
		fail(w, 409, "Нет проверенной ЭЦП для этой версии. Сначала подпишите документ.")
		return
	}
	// A card is a copy of the most recent signed snapshot. Earlier snapshots may differ.
	if fileID == "" {
		m.Signatures = m.Signatures[len(m.Signatures)-1:]
		ids = ids[len(ids)-1:]
		var rawDefs []byte
		if err = s.db.QueryRow(ctx, "SELECT dt.fields FROM document_types dt JOIN documents d ON d.type_id=dt.id WHERE d.id=$1", docID).Scan(&rawDefs); err != nil {
			errDB(w, err)
			return
		}
		var defs []FieldDef
		if err = json.Unmarshal(rawDefs, &defs); err != nil {
			errDB(w, err)
			return
		}
		display := map[string]any{}
		for key, value := range m.Fields {
			label := key
			for _, field := range defs {
				if field.Key != key {
					continue
				}
				label = field.Label
				if field.Type == "user" || field.Type == "users" {
					values := []string{}
					switch v := value.(type) {
					case string:
						values = append(values, v)
					case []any:
						for _, item := range v {
							if id, ok := item.(string); ok {
								values = append(values, id)
							}
						}
					}
					names := []string{}
					for _, id := range values {
						var name string
						if s.db.QueryRow(ctx, "SELECT full_name FROM users WHERE id::text=$1", id).Scan(&name) == nil {
							names = append(names, name)
						} else {
							names = append(names, "Сотрудник не найден")
						}
					}
					value = strings.Join(names, ", ")
				}
				break
			}
			display[label] = value
		}
		m.Fields = display
	}
	base := strings.TrimRight(env("PUBLIC_VERIFY_BASE_URL", s.origin), "/")
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		fail(w, 503, "Настройте PUBLIC_VERIFY_BASE_URL: HTTPS-адрес сервера проверки")
		return
	}
	token := randomToken()
	m.VerifyURL = base + "/verify/" + token
	dir, err := os.MkdirTemp("", "docjet-pdf-")
	if err != nil {
		errDB(w, err)
		return
	}
	defer os.RemoveAll(dir)
	if source != "" && kind != "application/pdf" {
		converted := filepath.Join(dir, "source.pdf")
		// Work on a temporary copy; Word must never touch the stored original.
		data, e := os.ReadFile(source)
		if e != nil {
			errDB(w, e)
			return
		}
		input := filepath.Join(dir, "source.docx")
		if e = os.WriteFile(input, data, 0600); e != nil {
			errDB(w, e)
			return
		}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			script := filepath.Join(dir, "convert.ps1")
			_ = os.WriteFile(script, wordConverter, 0600)
			cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-Source", input, "-Target", converted)
		} else {
			cmd = exec.CommandContext(ctx, env("DOCJET_SOFFICE", "soffice"), "-env:UserInstallation="+(&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "lo-profile"))}).String(), "--headless", "--convert-to", "pdf", "--outdir", dir, input)
		}
		if output, e := cmd.CombinedOutput(); e != nil {
			log.Printf("PDF converter failed: %v (%d output bytes)", e, len(output))
			fail(w, 503, "Не удалось преобразовать DOCX. На Windows требуется Microsoft Word, на Linux — LibreOffice.")
			return
		}
		source = converted
	}
	raw, _ := json.Marshal(m)
	metaPath := filepath.Join(dir, "metadata.json")
	script := filepath.Join(dir, "render.py")
	out := filepath.Join(dir, "result.pdf")
	if err = os.WriteFile(metaPath, raw, 0600); err == nil {
		err = os.WriteFile(script, pdfRenderer, 0600)
	}
	if err != nil {
		errDB(w, err)
		return
	}
	args := []string{script, metaPath, out}
	if source != "" {
		args = append(args, source)
	}
	cmd := exec.CommandContext(ctx, env("DOCJET_PDF_PYTHON", "python3"), args...)
	if output, e := cmd.CombinedOutput(); e != nil {
		log.Printf("PDF render failed: %v: %s", e, string(output))
		fail(w, 503, "Не удалось создать PDF. Проверьте формат файла и настройку PDF-сервиса.")
		return
	}
	data, err := os.ReadFile(out)
	if err != nil {
		errDB(w, err)
		return
	}
	// Only public verification data is persisted. Document content stays private.
	m.Summary = ""
	m.Fields = nil
	publicRaw, _ := json.Marshal(m)
	idRaw, _ := json.Marshal(ids)
	_, err = s.db.Exec(ctx, "INSERT INTO pdf_exports(token_hash,document_id,file_id,signature_ids,metadata,pdf_sha256) VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6)", digest([]byte(token)), docID, fileID, idRaw, publicRaw, digest(data))
	if err != nil {
		errDB(w, err)
		return
	}
	filename := strings.TrimSuffix(m.Filename, filepath.Ext(m.Filename)) + " — DocJet.pdf"
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filename))
	w.Write(data)
}
func (s *Server) publicPDFVerification(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	w.Header().Set("Cache-Control", "no-store")
	if !receiptToken.MatchString(token) {
		fail(w, 404, "Выгрузка не найдена")
		return
	}
	var meta json.RawMessage
	var hash string
	var checked *time.Time
	var valid *bool
	err := s.db.QueryRow(r.Context(), `SELECT e.metadata,e.pdf_sha256,e.checked_at,e.check_valid FROM pdf_exports e JOIN documents d ON d.id=e.document_id WHERE e.token_hash=$1 AND d.deleted_at IS NULL`, digest([]byte(token))).Scan(&meta, &hash, &checked, &valid)
	if err != nil {
		fail(w, 404, "Выгрузка не найдена")
		return
	}
	respond(w, 200, map[string]any{"metadata": meta, "pdf_sha256": hash, "checked_at": checked, "check_valid": valid})
}
func (s *Server) recheckPDFSignature(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	token := r.PathValue("token")
	if !receiptToken.MatchString(token) {
		fail(w, 404, "Выгрузка не найдена")
		return
	}
	select {
	case verifySlots <- struct{}{}:
		defer func() { <-verifySlots }()
	default:
		fail(w, 429, "Проверка занята, повторите позже")
		return
	}
	// Atomic throttle; an unavailable verifier is not recorded as a valid signature.
	var ids []byte
	var checkedAt time.Time
	err := s.db.QueryRow(ctx, `UPDATE pdf_exports e SET checked_at=now(),check_valid=NULL FROM documents d WHERE e.document_id=d.id AND d.deleted_at IS NULL AND e.token_hash=$1 AND (e.checked_at IS NULL OR e.checked_at < now()-interval '1 minute') RETURNING e.signature_ids,e.checked_at`, digest([]byte(token))).Scan(&ids, &checkedAt)
	if err != nil {
		fail(w, 429, "Проверка недоступна либо уже выполнялась. Повторите через минуту.")
		return
	}
	var sigIDs []string
	if json.Unmarshal(ids, &sigIDs) != nil || len(sigIDs) == 0 {
		fail(w, 404, "Подписи не найдены")
		return
	}
	for _, id := range sigIDs {
		var cms, payload []byte
		var iin, hash string
		err = s.db.QueryRow(ctx, "SELECT cms,payload,signer_iin,payload_sha256 FROM document_signatures WHERE id=$1", id).Scan(&cms, &payload, &iin, &hash)
		if err != nil {
			fail(w, 503, "Сохранённая подпись недоступна")
			return
		}
		if digest(payload) != hash {
			fail(w, 409, "Нарушена целостность подписанного пакета")
			return
		}
		if _, _, err = verifyNCA(ctx, cms, payload, iin); err != nil {
			fail(w, 503, "Подтвердить ЭЦП сейчас не удалось. Возможны недоступность НУЦ РК, истечение или отзыв сертификата.")
			return
		}
	}
	result, err := s.db.Exec(ctx, "UPDATE pdf_exports SET check_valid=true WHERE token_hash=$1 AND checked_at=$2", digest([]byte(token)), checkedAt)
	if err != nil {
		errDB(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		fail(w, 409, "Начата более поздняя проверка. Обновите сведения.")
		return
	}
	respond(w, 200, map[string]any{"valid": true})
}
