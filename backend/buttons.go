package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type WorkflowButton struct {
	Code            string `json:"code"`
	Label           string `json:"label"`
	Effect          string `json:"effect"`
	Style           string `json:"style"`
	CommentRequired bool   `json:"comment_required"`
	Active          bool   `json:"active"`
}

func legacyButtons() []WorkflowButton {
	return []WorkflowButton{
		{Code: "approve", Label: "Согласовать", Effect: "complete", Style: "primary", Active: true},
		{Code: "return", Label: "Вернуть на доработку", Effect: "return", Style: "subtle", CommentRequired: true, Active: true},
		{Code: "reject", Label: "Отклонить", Effect: "reject", Style: "danger-outline", CommentRequired: true, Active: true},
	}
}
func stepButtons(step Step) []WorkflowButton {
	if step.Actions == nil && step.ButtonCodes == nil {
		return legacyButtons()
	}
	return step.Actions
}
func (s *Server) prepareFlow(ctx context.Context, f *Flow) string {
	for i := range f.Steps {
		st := &f.Steps[i]
		if st.Kind == "" {
			st.Kind = "approval"
		}
		if st.ButtonCodes == nil {
			st.ButtonCodes = []string{"approve", "return", "reject"}
		}
		st.Actions = []WorkflowButton{}
		seen := map[string]bool{}
		completes := false
		if len(st.ButtonCodes) == 0 || len(st.ButtonCodes) > 12 {
			return fmt.Sprintf("Этап %d: выберите от 1 до 12 кнопок", i+1)
		}
		for _, code := range st.ButtonCodes {
			if seen[code] {
				return fmt.Sprintf("Этап %d: кнопка повторяется", i+1)
			}
			seen[code] = true
			var b WorkflowButton
			err := s.db.QueryRow(ctx, "SELECT code,label,effect,style,comment_required,active FROM workflow_buttons WHERE code=$1", code).Scan(&b.Code, &b.Label, &b.Effect, &b.Style, &b.CommentRequired, &b.Active)
			if err != nil || !b.Active {
				return fmt.Sprintf("Этап %d: кнопка %s недоступна. Выберите активную кнопку", i+1, code)
			}
			if b.Effect == "complete" {
				completes = true
			}
			st.Actions = append(st.Actions, b)
		}
		if !completes {
			return fmt.Sprintf("Этап %d: нужна хотя бы одна кнопка завершения задачи", i+1)
		}
	}
	return ""
}
func (s *Server) listButtons(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	rows, err := s.db.Query(r.Context(), "SELECT code,label,effect,style,comment_required,active FROM workflow_buttons ORDER BY label")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []WorkflowButton{}
	for rows.Next() {
		var b WorkflowButton
		if err = rows.Scan(&b.Code, &b.Label, &b.Effect, &b.Style, &b.CommentRequired, &b.Active); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, b)
	}
	if err = rows.Err(); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *Server) saveButton(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var b WorkflowButton
	if !decode(w, r, &b) {
		return
	}
	if r.Method == "PUT" {
		b.Code = r.PathValue("code")
	}
	b.Label = strings.TrimSpace(b.Label)
	if !codePattern.MatchString(b.Code) || b.Label == "" || len([]rune(b.Label)) > 80 {
		fail(w, 400, "Укажите код латиницей (2–49 символов) и название до 80 символов")
		return
	}
	switch b.Effect {
	case "complete", "start", "return", "reject":
	default:
		fail(w, 400, "Неизвестное действие кнопки")
		return
	}
	switch b.Style {
	case "primary", "subtle", "danger-outline":
	default:
		fail(w, 400, "Неизвестный стиль кнопки")
		return
	}
	if b.Effect == "return" || b.Effect == "reject" {
		b.CommentRequired = true
	}
	if r.Method == "POST" {
		_, err := s.db.Exec(r.Context(), "INSERT INTO workflow_buttons(code,label,effect,style,comment_required,active) VALUES($1,$2,$3,$4,$5,$6)", b.Code, b.Label, b.Effect, b.Style, b.CommentRequired, b.Active)
		if err != nil {
			errDB(w, err)
			return
		}
	} else {
		tag, err := s.db.Exec(r.Context(), "UPDATE workflow_buttons SET label=$2,effect=$3,style=$4,comment_required=$5,active=$6 WHERE code=$1", b.Code, b.Label, b.Effect, b.Style, b.CommentRequired, b.Active)
		if err != nil {
			errDB(w, err)
			return
		}
		if tag.RowsAffected() == 0 {
			fail(w, 404, "Кнопка не найдена")
			return
		}
	}
	respond(w, 200, b)
}
