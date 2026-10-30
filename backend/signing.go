package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var iinPattern = regexp.MustCompile(`^[0-9]{12}$`)
var ncaClient = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return fmt.Errorf("redirect refused") }}

// NCALayer versions return either PEM (CMS/PKCS7) or Base64 with line wrapping.
// Decode the transport representation; all cryptographic checks still apply.
func decodeCMS(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if strings.HasPrefix(encoded, "-----BEGIN ") {
		block, rest := pem.Decode([]byte(encoded))
		if block == nil || (block.Type != "CMS" && block.Type != "PKCS7") || len(bytes.TrimSpace(rest)) != 0 {
			return nil, fmt.Errorf("invalid CMS PEM")
		}
		return block.Bytes, nil
	}
	encoded = strings.NewReplacer("\r", "", "\n", "", "\t", "", " ", "").Replace(encoded)
	return base64.StdEncoding.DecodeString(encoded)
}

type ncaCertificate struct {
	Valid     bool      `json:"valid"`
	KeyUsage  string    `json:"keyUsage"`
	Serial    string    `json:"serialNumber"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	Subject   struct {
		IIN     string `json:"iin"`
		Name    string `json:"commonName"`
		Country string `json:"country"`
	} `json:"subject"`
	Revocations []struct {
		By      string `json:"by"`
		Revoked bool   `json:"revoked"`
	} `json:"revocations"`
}
type ncaReport struct {
	Status  int  `json:"status"`
	Valid   bool `json:"valid"`
	Signers []struct {
		Status       string           `json:"status"`
		Certificates []ncaCertificate `json:"certificates"`
	} `json:"signers"`
}

func verifierURL() string { return strings.TrimRight(os.Getenv("NCANODE_URL"), "/") }
func (s *Server) signingStatus(w http.ResponseWriter, r *http.Request) {
	ready := false
	if base := verifierURL(); base != "" {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", base+"/actuator/health", nil)
		if req != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			req = req.WithContext(ctx)
			resp, err := ncaClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
				var v struct {
					Status string `json:"status"`
				}
				ready = resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&v) == nil && v.Status == "UP"
			}
		}
	}
	var iin string
	_ = s.db.QueryRow(r.Context(), "SELECT signing_iin FROM users WHERE id=$1", me(r).ID).Scan(&iin)
	respond(w, 200, map[string]any{"configured": verifierURL() != "", "available": ready, "identity_configured": iinPattern.MatchString(iin), "mode": "detached_cms_manifest"})
}

// NCANode only applies detached data when CMS has no encapsulated content.
// Enforce this before verification so a signature over unrelated embedded data
// can never be accepted as a signature of our challenge.
func detachedCMS(cms []byte) bool {
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"explicit,tag:0"`
	}
	rest, err := asn1.Unmarshal(cms, &ci)
	if err != nil || len(rest) != 0 || !ci.Type.Equal(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}) {
		return false
	}
	var sd struct {
		Version int
		Digests asn1.RawValue
		Content struct {
			Type asn1.ObjectIdentifier
			Data asn1.RawValue `asn1:"optional,explicit,tag:0"`
		}
		Certificates asn1.RawValue `asn1:"optional,tag:0"`
		CRLs         asn1.RawValue `asn1:"optional,tag:1"`
		Signers      asn1.RawValue
	}
	rest, err = asn1.Unmarshal(ci.Content.Bytes, &sd)
	return err == nil && len(rest) == 0 && sd.Content.Type.Equal(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}) && len(sd.Content.Data.FullBytes) == 0
}

func verifyNCA(ctx context.Context, cms, payload []byte, expectedIIN string) (ncaCertificate, json.RawMessage, error) {
	var empty ncaCertificate
	base := verifierURL()
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return empty, nil, routeError("Сервер проверки ЭЦП не настроен")
	}
	if !detachedCMS(cms) {
		return empty, nil, routeError("Ожидается отдельная CMS-подпись подготовленного пакета документа")
	}
	body, _ := json.Marshal(map[string]any{"cms": base64.StdEncoding.EncodeToString(cms), "data": base64.StdEncoding.EncodeToString(payload), "revocationCheck": []string{"OCSP", "CRL"}})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/cms/verify", bytes.NewReader(body))
	if err != nil {
		return empty, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ncaClient.Do(req)
	if err != nil {
		return empty, nil, routeError("Сервис проверки НУЦ РК недоступен. Этап не завершён")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || resp.StatusCode != 200 {
		return empty, nil, routeError("Не удалось проверить ЭЦП через сервис НУЦ РК")
	}
	var report ncaReport
	if json.Unmarshal(raw, &report) != nil || report.Status != 200 || !report.Valid || len(report.Signers) != 1 || report.Signers[0].Status != "VALID" {
		return empty, nil, routeError("ЭЦП недействительна или проверка доверия/отзыва не завершена")
	}
	now := time.Now()
	for _, cert := range report.Signers[0].Certificates {
		if cert.KeyUsage != "SIGN" {
			continue
		}
		if !cert.Valid || cert.Subject.Country != "KZ" || cert.Subject.IIN != expectedIIN || !iinPattern.MatchString(expectedIIN) || cert.Serial == "" || cert.NotBefore.After(now) || !cert.NotAfter.After(now) {
			return empty, nil, routeError("Сертификат подписи недействителен или ИИН не совпадает с сотрудником")
		}
		checked := false
		for _, rev := range cert.Revocations {
			if rev.Revoked {
				return empty, nil, routeError("Сертификат отозван")
			}
			if rev.By == "OCSP" || rev.By == "CRL" {
				checked = true
			}
		}
		if !checked {
			return empty, nil, routeError("Не подтверждён статус отзыва сертификата")
		}
		return cert, json.RawMessage(raw), nil
	}
	return empty, nil, routeError("Не найден сертификат НУЦ РК для подписи")
}

func (s *Server) prepareSigning(w http.ResponseWriter, r *http.Request) {
	if verifierURL() == "" {
		fail(w, 503, "Сервер проверки ЭЦП не настроен")
		return
	}
	taskID := r.PathValue("id")
	p := me(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var docID, instance, instanceStatus, taskStatus, userID, iin string
	var step, current int
	var raw []byte
	err = tx.QueryRow(r.Context(), `SELECT wi.document_id,wi.id,wi.status,wi.current_step,wi.definition_snapshot,t.step_index,t.status,t.assignee_id,u.signing_iin FROM workflow_tasks t JOIN workflow_instances wi ON wi.id=t.instance_id JOIN documents d ON d.id=wi.document_id JOIN users u ON u.id=t.assignee_id WHERE t.id=$1 AND d.deleted_at IS NULL FOR UPDATE OF wi,d,t`, taskID).Scan(&docID, &instance, &instanceStatus, &current, &raw, &step, &taskStatus, &userID, &iin)
	if err != nil {
		errDB(w, err)
		return
	}
	if userID != p.ID {
		fail(w, 403, "Вы не назначены подписантом")
		return
	}
	var f Flow
	if json.Unmarshal(raw, &f) != nil || step < 0 || step >= len(f.Steps) || f.Steps[step].Kind != "signing" {
		fail(w, 400, "Задача не является этапом подписания")
		return
	}
	if instanceStatus != "active" || (taskStatus != "pending" && taskStatus != "in_progress") || step < current || step >= parallelEnd(f.Steps, current) {
		fail(w, 409, "Этап подписания уже завершён или не активен")
		return
	}
	if !iinPattern.MatchString(iin) {
		fail(w, 409, "Администратор должен указать ИИН подписанта в карточке сотрудника")
		return
	}
	payload, err := s.signingPayload(r.Context(), tx, docID, instance, taskID)
	if err != nil {
		errDB(w, err)
		return
	}
	expires := time.Now().UTC().Add(10 * time.Minute)
	var id string
	err = tx.QueryRow(r.Context(), "INSERT INTO signing_challenges(document_id,task_id,user_id,payload,expires_at) VALUES($1,$2,$3,$4,$5) RETURNING id", docID, taskID, p.ID, payload, expires).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	hash := sha256.Sum256(payload)
	respond(w, 201, map[string]any{"challenge_id": id, "data": base64.StdEncoding.EncodeToString(payload), "payload": json.RawMessage(payload), "sha256": hex.EncodeToString(hash[:]), "expires_at": expires})
}

func (s *Server) signingPayload(ctx context.Context, tx pgx.Tx, docID, instance, task string) ([]byte, error) {
	var doc json.RawMessage
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object('id',d.id,'type',dt.name,'title',d.title,'summary',d.summary,'fields',d.fields,'project_number',d.project_number,'registration_number',d.registration_number,'registered_at',d.registered_at,'author_id',d.author_id,'assignee_id',d.assignee_id,'department_id',d.department_id,'due_at',d.due_at,'created_at',d.created_at) FROM documents d JOIN document_types dt ON dt.id=d.type_id WHERE d.id=$1`, docID).Scan(&doc)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON(filename) id,filename,version,storage_key FROM files WHERE document_id=$1 ORDER BY filename,version DESC`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []map[string]any{}
	for rows.Next() {
		var id, name, key string
		var version int
		if err = rows.Scan(&id, &name, &version, &key); err != nil {
			return nil, err
		}
		file, err := os.Open(safePath(s.storage, key))
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, copyErr := io.Copy(h, io.LimitReader(file, maxUpload+1))
		file.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		files = append(files, map[string]any{"id": id, "filename": name, "version": version, "sha256": hex.EncodeToString(h.Sum(nil))})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"format": "docflow-signing-v1", "document": doc, "workflow_instance_id": instance, "task_id": task, "files": files, "nonce": randomToken(), "prepared_at": time.Now().UTC().Format(time.RFC3339Nano)})
}

func (s *Server) acceptSignature(ctx context.Context, tx pgx.Tx, docID, taskID, userID, challengeID, encoded string) error {
	if !goodID(challengeID) || len(encoded) == 0 || len(encoded) > 700000 {
		return routeError("Подготовьте документ и подпишите его через NCALayer")
	}
	cms, err := decodeCMS(encoded)
	if err != nil || len(cms) > 512<<10 {
		return routeError("Некорректный размер или формат CMS-подписи")
	}
	var payload []byte
	var iin string
	err = tx.QueryRow(ctx, `SELECT c.payload,u.signing_iin FROM signing_challenges c JOIN users u ON u.id=c.user_id WHERE c.id=$1 AND c.task_id=$2 AND c.user_id=$3 AND c.document_id=$4 AND c.used_at IS NULL AND c.expires_at>now() FOR UPDATE OF c`, challengeID, taskID, userID, docID).Scan(&payload, &iin)
	if err != nil {
		return routeError("Запрос подписи истёк или уже использован. Подготовьте документ заново")
	}
	// Compare current document and file content to the challenge, excluding its nonce/time.
	current, err := s.signingPayload(ctx, tx, docID, "", taskID)
	if err != nil {
		return err
	}
	var original, latest map[string]json.RawMessage
	if json.Unmarshal(payload, &original) != nil || json.Unmarshal(current, &latest) != nil {
		return routeError("Повреждён пакет подписи")
	}
	if !bytes.Equal(original["document"], latest["document"]) || !bytes.Equal(original["files"], latest["files"]) {
		return routeError("Документ или вложения изменились. Подготовьте подпись заново")
	}
	cert, report, err := verifyNCA(ctx, cms, payload, iin)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(payload)
	_, err = tx.Exec(ctx, `INSERT INTO document_signatures(document_id,task_id,challenge_id,signer_id,signer_iin,signer_name,certificate_serial,cms,payload,payload_sha256,verification) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, docID, taskID, challengeID, userID, iin, cert.Subject.Name, cert.Serial, cms, payload, hex.EncodeToString(hash[:]), report)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE signing_challenges SET used_at=now() WHERE id=$1", challengeID); err != nil {
		return err
	}
	return event(ctx, tx, docID, userID, "document.signed", map[string]any{"action_label": "Подписан ЭЦП НУЦ РК", "signer_name": cert.Subject.Name, "certificate_serial": cert.Serial, "task_id": taskID, "payload_sha256": hex.EncodeToString(hash[:])})
}

func (s *Server) listSignatures(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	rows, err := s.db.Query(r.Context(), "SELECT id,task_id,signer_name,certificate_serial,payload_sha256,verified_at FROM document_signatures WHERE document_id=$1 ORDER BY verified_at", id)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var sid, task, name, serial, hash string
		var at time.Time
		if err = rows.Scan(&sid, &task, &name, &serial, &hash, &at); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, map[string]any{"id": sid, "task_id": task, "signer_name": name, "certificate_serial": serial, "sha256": hash, "verified_at": at})
	}
	if err = rows.Err(); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *Server) downloadSignature(w http.ResponseWriter, r *http.Request) {
	var doc string
	var cms, payload []byte
	err := s.db.QueryRow(r.Context(), "SELECT document_id,cms,payload FROM document_signatures WHERE id=$1", r.PathValue("id")).Scan(&doc, &cms, &payload)
	if err != nil {
		errDB(w, err)
		return
	}
	if !s.readAllowed(r.Context(), doc, me(r)) {
		fail(w, 404, "Подпись не найдена")
		return
	}
	data := cms
	name := "signature.p7s"
	kind := "application/pkcs7-signature"
	if r.URL.Query().Get("format") == "payload" {
		data = payload
		name = "signed-document.json"
		kind = "application/json"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write(data)
}
