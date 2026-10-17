package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) delegateTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AssigneeID string `json:"assignee_id"`
		Comment    string `json:"comment"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !goodID(in.AssigneeID) || len([]rune(in.Comment)) > 2000 {
		fail(w, 400, "Выберите сотрудника и укажите комментарий не длиннее 2000 символов")
		return
	}
	taskID := r.PathValue("id")
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var instance string
	if err = tx.QueryRow(r.Context(), "SELECT instance_id FROM workflow_tasks WHERE id=$1", taskID).Scan(&instance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "Задача не найдена")
		} else {
			errDB(w, err)
		}
		return
	}
	var docID, instanceStatus string
	var currentStep int
	var snapshot []byte
	if err = tx.QueryRow(r.Context(), "SELECT document_id,status,current_step,definition_snapshot FROM workflow_instances WHERE id=$1 FOR UPDATE", instance).Scan(&docID, &instanceStatus, &currentStep, &snapshot); err != nil {
		errDB(w, err)
		return
	}
	var assignee, status, stepID, title, parent string
	var stepIndex int
	if err = tx.QueryRow(r.Context(), "SELECT assignee_id,status,step_index,step_id,title,coalesce(delegated_from::text,'') FROM workflow_tasks WHERE id=$1 FOR UPDATE", taskID).Scan(&assignee, &status, &stepIndex, &stepID, &title, &parent); err != nil {
		errDB(w, err)
		return
	}
	if assignee != me(r).ID {
		fail(w, 403, "Поручить задачу может только её исполнитель")
		return
	}
	if parent != "" {
		fail(w, 409, "Порученную задачу нельзя поручить повторно")
		return
	}
	var flow Flow
	if err = json.Unmarshal(snapshot, &flow); err != nil {
		errDB(w, err)
		return
	}
	if instanceStatus != "active" || (status != "pending" && status != "in_progress") || stepIndex < currentStep || stepIndex >= parallelEnd(flow.Steps, currentStep) {
		fail(w, 409, "Задача уже закрыта или этап завершён")
		return
	}
	if in.AssigneeID == assignee {
		fail(w, 400, "Выберите другого сотрудника")
		return
	}
	var active bool
	if err = tx.QueryRow(r.Context(), "SELECT active FROM users WHERE id=$1", in.AssigneeID).Scan(&active); err != nil || !active {
		fail(w, 400, "Сотрудник не найден или заблокирован")
		return
	}
	var exists bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM workflow_tasks WHERE delegated_from=$1 OR (instance_id=$2 AND step_index=$3 AND assignee_id=$4))", taskID, instance, stepIndex, in.AssigneeID).Scan(&exists); err != nil {
		errDB(w, err)
		return
	}
	if exists {
		fail(w, 409, "Сотрудник уже участвует в этом этапе или задача уже поручена")
		return
	}
	var delegatedID string
	if err = tx.QueryRow(r.Context(), "INSERT INTO workflow_tasks(instance_id,document_id,step_index,step_id,title,assignee_id,delegated_from) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id", instance, docID, stepIndex, stepID, title, in.AssigneeID, taskID).Scan(&delegatedID); err != nil {
		errDB(w, err)
		return
	}
	comment := strings.TrimSpace(in.Comment)
	if err = event(r.Context(), tx, docID, me(r).ID, "workflow.task_delegated", map[string]any{"task_id": taskID, "delegated_task_id": delegatedID, "assignee_id": in.AssigneeID, "comment": comment}); err != nil {
		errDB(w, err)
		return
	}
	if err = emit(r.Context(), tx, "workflow.task_delegated", map[string]any{"document_id": docID, "task_id": taskID, "delegated_task_id": delegatedID, "assignee_id": in.AssigneeID}); err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": delegatedID})
}
