package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Journal struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Pattern string `json:"number_pattern"`
	Width   int    `json:"number_width"`
	Next    int64  `json:"next_number"`
	Active  bool   `json:"active"`
}

func validateJournal(v Journal) string {
	if strings.TrimSpace(v.Name) == "" || len([]rune(v.Name)) > 150 {
		return "Укажите название журнала до 150 символов"
	}
	if v.Kind != "project" && v.Kind != "registration" {
		return "Выберите вид журнала"
	}
	if len([]rune(v.Pattern)) > 100 || strings.Count(v.Pattern, "{seq}") != 1 {
		return "Формат должен содержать один {seq} и не более 100 символов"
	}
	rest := strings.NewReplacer("{seq}", "", "{year}", "").Replace(v.Pattern)
	if strings.ContainsAny(rest, "{}\r\n") {
		return "Допустимые переменные: {seq} и {year}"
	}
	if v.Width < 1 || v.Width > 12 || v.Next < 1 || v.Next > 999999999998 {
		return "Разрядность: 1–12, следующий номер: 1–999999999998"
	}
	return ""
}

func formatJournalNumber(v Journal, at time.Time) string {
	return strings.NewReplacer("{seq}", fmt.Sprintf("%0*d", v.Width, v.Next), "{year}", at.Format("2006")).Replace(v.Pattern)
}

func (s *Server) listJournals(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), "SELECT id,name,kind,number_pattern,number_width,next_number,active FROM registration_journals ORDER BY kind,name")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []Journal{}
	for rows.Next() {
		var v Journal
		if err = rows.Scan(&v.ID, &v.Name, &v.Kind, &v.Pattern, &v.Width, &v.Next, &v.Active); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, out)
}

func (s *Server) saveJournal(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var v Journal
	if !decode(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	v.Pattern = strings.TrimSpace(v.Pattern)
	if msg := validateJournal(v); msg != "" {
		fail(w, 400, msg)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	status := 201
	if r.Method == "POST" {
		err = tx.QueryRow(r.Context(), "INSERT INTO registration_journals(name,kind,number_pattern,number_width,next_number,active) VALUES($1,$2,$3,$4,$5,$6) RETURNING id", v.Name, v.Kind, v.Pattern, v.Width, v.Next, v.Active).Scan(&v.ID)
	} else {
		v.ID = r.PathValue("id")
		var kind string
		var next int64
		err = tx.QueryRow(r.Context(), "SELECT kind,next_number FROM registration_journals WHERE id=$1 FOR UPDATE", v.ID).Scan(&kind, &next)
		if err != nil {
			errDB(w, err)
			return
		}
		if kind != v.Kind {
			fail(w, 400, "Вид существующего журнала изменить нельзя")
			return
		}
		if v.Next < next {
			fail(w, 409, "Следующий номер уже изменился или был выдан. Обновите журнал; уменьшение счётчика запрещено")
			return
		}
		_, err = tx.Exec(r.Context(), "UPDATE registration_journals SET name=$2,number_pattern=$3,number_width=$4,next_number=$5,active=$6 WHERE id=$1", v.ID, v.Name, v.Pattern, v.Width, v.Next, v.Active)
		status = 200
	}
	if err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, status, v)
}

func (s *Server) validJournals(ctx context.Context, project, registration string) bool {
	for kind, id := range map[string]string{"project": project, "registration": registration} {
		if id == "" {
			continue
		}
		if !goodID(id) {
			return false
		}
		var ok bool
		if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM registration_journals WHERE id=$1 AND kind=$2 AND active)", id, kind).Scan(&ok); err != nil || !ok {
			return false
		}
	}
	return true
}

// The caller holds the document lock. The journal row serializes number allocation
// across documents; counter, number, entry and audit event commit together.
func (s *Server) assignJournalNumber(ctx context.Context, tx pgx.Tx, docID, actor, kind string) error {
	var number, journal string
	query := `SELECT coalesce(d.project_number,''),coalesce(d.project_journal_id,dt.project_journal_id)::text FROM documents d JOIN document_types dt ON dt.id=d.type_id WHERE d.id=$1 FOR UPDATE OF d`
	if kind == "registration" {
		query = `SELECT CASE WHEN d.registered_at IS NULL THEN '' ELSE coalesce(d.registration_number,'') END,coalesce(d.registration_journal_id,dt.registration_journal_id)::text FROM documents d JOIN document_types dt ON dt.id=d.type_id WHERE d.id=$1 FOR UPDATE OF d`
	}
	var journalID *string
	if err := tx.QueryRow(ctx, query, docID).Scan(&number, &journalID); err != nil {
		return err
	}
	if number != "" {
		return nil
	}
	if journalID == nil {
		if kind == "project" {
			return nil
		}
		return routeError("Для типа документа не выбран регистрационный журнал")
	}
	journal = *journalID
	var v Journal
	err := tx.QueryRow(ctx, "SELECT id,name,kind,number_pattern,number_width,next_number,active FROM registration_journals WHERE id=$1 FOR UPDATE", journal).Scan(&v.ID, &v.Name, &v.Kind, &v.Pattern, &v.Width, &v.Next, &v.Active)
	if err != nil {
		return err
	}
	if !v.Active || v.Kind != kind {
		return routeError("Журнал отключён или не соответствует виду номера")
	}
	if v.Next >= 999999999999 {
		return routeError("Счётчик журнала исчерпан")
	}
	var at time.Time
	if err = tx.QueryRow(ctx, "SELECT now() AT TIME ZONE 'Asia/Qyzylorda'").Scan(&at); err != nil {
		return err
	}
	number = formatJournalNumber(v, at)
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM journal_entries WHERE journal_id=$1 AND number=$2)", journal, number).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return routeError("Номер уже существует в журнале. Измените формат или следующий номер")
	}
	_, err = tx.Exec(ctx, "INSERT INTO journal_entries(journal_id,document_id,kind,number,sequence_number,actor_id) VALUES($1,$2,$3,$4,$5,$6)", journal, docID, kind, number, v.Next, nullableStr(actor))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE registration_journals SET next_number=next_number+1 WHERE id=$1", journal); err != nil {
		return err
	}
	query = "UPDATE documents SET project_number=$2,project_journal_id=$3,project_numbered_at=now(),updated_at=now() WHERE id=$1"
	if kind == "registration" {
		query = "UPDATE documents SET registration_number=$2,registration_journal_id=$3,registered_at=now(),updated_at=now() WHERE id=$1"
	}
	if _, err = tx.Exec(ctx, query, docID, number, journal); err != nil {
		return err
	}
	return event(ctx, tx, docID, actor, "document."+kind+"_number_assigned", map[string]any{"number": number, "journal_id": journal, "journal_name": v.Name})
}

func journalError(w http.ResponseWriter, err error) {
	var invalid routeError
	if errors.As(err, &invalid) {
		fail(w, 409, string(invalid))
	} else {
		errDB(w, err)
	}
}

func (s *Server) registrationStepDone(ctx context.Context, tx pgx.Tx, instance string, step int, mode string) (bool, error) {
	var pending, finished, total int
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE active),count(*) FILTER(WHERE finished),count(*) FROM (
 SELECT bool_or(status IN ('pending','in_progress')) AS active,bool_or(status IN ('approved','completed')) AS finished
 FROM workflow_tasks WHERE instance_id=$1 AND step_index=$2 GROUP BY coalesce(delegated_from,id)
 ) obligations`, instance, step).Scan(&pending, &finished, &total)
	return total > 0 && ((mode == "any" && finished > 0) || (mode == "all" && pending == 0 && finished == total)), err
}

func (s *Server) journalEntries(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin", "registrar") {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT e.document_id,e.number,e.assigned_at,d.title,d.deleted_at IS NOT NULL FROM journal_entries e JOIN documents d ON d.id=e.document_id WHERE e.journal_id=$1 ORDER BY e.id DESC LIMIT 200`, r.PathValue("id"))
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, number, title string
		var at time.Time
		var deleted bool
		if err = rows.Scan(&id, &number, &at, &title, &deleted); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, map[string]any{"document_id": id, "number": number, "assigned_at": at, "title": title, "deleted": deleted})
	}
	if err = rows.Err(); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, out)
}
