package main

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type AssigneeSource struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}
type routeError string

func (e routeError) Error() string { return string(e) }

func parallelEnd(steps []Step, from int) int {
	if from >= len(steps) {
		return from
	}
	group := steps[from].ParallelGroup
	if group == "" {
		return from + 1
	}
	end := from + 1
	for end < len(steps) && steps[end].ParallelGroup == group {
		end++
	}
	return end
}

func (s *Server) resolveAssignees(ctx context.Context, tx pgx.Tx, step Step, docID string, fields map[string]any) ([]string, error) {
	sources := step.Assignees
	if len(sources) == 0 {
		for _, id := range step.AssigneeIDs {
			sources = append(sources, AssigneeSource{Kind: "user", ID: id})
		}
	}
	ids := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, src := range sources {
		switch src.Kind {
		case "user":
			add(src.ID)
		case "document_field":
			value := fields[src.ID]
			found := false
			if id, ok := value.(string); ok && id != "" {
				add(id)
				found = true
			}
			if values, ok := value.([]any); ok {
				for _, item := range values {
					if id, ok := item.(string); ok && id != "" {
						add(id)
						found = true
					}
				}
			}
			if !found {
				return nil, routeError("В документе не заполнено поле пользователя «" + src.ID + "» для этапа «" + step.Title + "»")
			}
		case "author":
			var id string
			if err := tx.QueryRow(ctx, "SELECT author_id FROM documents WHERE id=$1", docID).Scan(&id); err != nil {
				return nil, err
			}
			add(id)
		case "assignee":
			var id string
			if err := tx.QueryRow(ctx, "SELECT coalesce(assignee_id::text,'') FROM documents WHERE id=$1", docID).Scan(&id); err != nil {
				return nil, err
			}
			if id == "" {
				return nil, routeError("Укажите ответственного в документе для этапа «" + step.Title + "»")
			}
			add(id)
		case "group":
			var active bool
			if err := tx.QueryRow(ctx, "SELECT active FROM workflow_groups WHERE id=$1", src.ID).Scan(&active); err != nil {
				return nil, routeError("Группа для этапа «" + step.Title + "» не найдена")
			}
			if !active {
				return nil, routeError("Группа для этапа «" + step.Title + "» отключена")
			}
			rows, err := tx.Query(ctx, "SELECT u.id FROM workflow_group_members gm JOIN users u ON u.id=gm.user_id WHERE gm.group_id=$1 AND u.active=true ORDER BY u.full_name", src.ID)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				add(id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		default:
			return nil, routeError("Некорректный источник исполнителей")
		}
	}
	if len(ids) == 0 {
		return nil, routeError("Нет исполнителей для этапа «" + step.Title + "»")
	}
	if len(ids) > 200 {
		return nil, routeError("Слишком много исполнителей в этапе «" + step.Title + "»")
	}
	for _, id := range ids {
		var active bool
		if err := tx.QueryRow(ctx, "SELECT active FROM users WHERE id=$1", id).Scan(&active); err != nil || !active {
			return nil, routeError("Исполнитель для этапа «" + step.Title + "» отсутствует или заблокирован")
		}
	}
	return ids, nil
}

func (s *Server) validateSourceRefs(ctx context.Context, flow Flow) string {
	for _, step := range flow.Steps {
		for _, src := range step.Assignees {
			if src.Kind == "user" {
				var active bool
				if err := s.db.QueryRow(ctx, "SELECT active FROM users WHERE id=$1", src.ID).Scan(&active); err != nil || !active {
					return "Пользователь для этапа «" + step.Title + "» отсутствует или заблокирован"
				}
			}
			if src.Kind == "group" {
				var exists bool
				if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workflow_groups WHERE id=$1 AND active=true)", src.ID).Scan(&exists); err != nil || !exists {
					return "Группа для этапа «" + step.Title + "» не найдена или отключена"
				}
			}
		}
	}
	return ""
}

func (s *Server) activate(ctx context.Context, tx pgx.Tx, instance, docID string, from int, flow Flow, fieldsRaw []byte, title string) error {
	fields := map[string]any{}
	if err := json.Unmarshal(fieldsRaw, &fields); err != nil {
		return err
	}
	fields["title"] = title
	for from < len(flow.Steps) {
		end := parallelEnd(flow.Steps, from)
		active := []int{}
		review := false
		for i := from; i < end; i++ {
			step := flow.Steps[i]
			if !conditionMatches(step.Condition, fields) {
				if err := event(ctx, tx, docID, "", "workflow.step_skipped", map[string]any{"step": step.ID}); err != nil {
					return err
				}
				continue
			}
			active = append(active, i)
			if step.Kind == "approval" || step.Kind == "" {
				review = true
			}
		}
		if len(active) == 0 {
			from = end
			continue
		}
		assignees := make(map[int][]string, len(active))
		for _, i := range active {
			ids, err := s.resolveAssignees(ctx, tx, flow.Steps[i], docID, fields)
			if err != nil {
				return err
			}
			assignees[i] = ids
		}
		if _, err := tx.Exec(ctx, "UPDATE workflow_instances SET current_step=$1 WHERE id=$2", from, instance); err != nil {
			return err
		}
		status := "in_progress"
		if review {
			status = "in_review"
		}
		if _, err := tx.Exec(ctx, "UPDATE documents SET status=$1,updated_at=now() WHERE id=$2", status, docID); err != nil {
			return err
		}
		for _, i := range active {
			step := flow.Steps[i]
			for _, id := range assignees[i] {
				if _, err := tx.Exec(ctx, "INSERT INTO workflow_tasks(instance_id,document_id,step_index,step_id,title,assignee_id) VALUES($1,$2,$3,$4,$5,$6)", instance, docID, i, step.ID, step.Title, id); err != nil {
					return err
				}
			}
			if err := event(ctx, tx, docID, "", "workflow.step_started", map[string]any{"step": step.ID, "index": i, "parallel_group": step.ParallelGroup}); err != nil {
				return err
			}
		}
		return nil
	}
	final := flow.CompletionStatus
	if final == "" {
		final = "approved"
	}
	if _, err := tx.Exec(ctx, "UPDATE workflow_instances SET status=$3,completed_at=now(),current_step=$1 WHERE id=$2", len(flow.Steps), instance, final); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE documents SET status=$2,updated_at=now() WHERE id=$1", docID, final); err != nil {
		return err
	}
	if err := event(ctx, tx, docID, "", "workflow."+final, map[string]any{"instance_id": instance}); err != nil {
		return err
	}
	return emit(ctx, tx, "workflow."+final, map[string]any{"document_id": docID})
}

func (s *Server) groupDone(ctx context.Context, tx pgx.Tx, instance string, flow Flow, from int) (bool, error) {
	end := parallelEnd(flow.Steps, from)
	for i := from; i < end; i++ {
		var pending, finished, total int
		err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE active),count(*) FILTER(WHERE finished),count(*) FROM (
		 SELECT bool_or(status IN ('pending','in_progress')) AS active,bool_or(status IN ('approved','completed')) AS finished
		 FROM workflow_tasks WHERE instance_id=$1 AND step_index=$2 GROUP BY coalesce(delegated_from,id)
		) obligations`, instance, i).Scan(&pending, &finished, &total)
		if err != nil {
			return false, err
		}
		if total == 0 {
			continue
		}
		mode := flow.Steps[i].Mode
		if mode == "any" && finished > 0 {
			if _, err = tx.Exec(ctx, "UPDATE workflow_tasks SET status='cancelled' WHERE instance_id=$1 AND step_index=$2 AND status IN ('pending','in_progress')", instance, i); err != nil {
				return false, err
			}
			continue
		}
		if mode == "all" && pending == 0 && finished == total {
			continue
		}
		return false, nil
	}
	return true, nil
}
