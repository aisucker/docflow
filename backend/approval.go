package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type ApprovalStage struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Mode    string   `json:"mode"`
	UserIDs []string `json:"user_ids"`
}

// The legacy users array remains readable when a field is changed to approval.
func parseApproval(value any) ([]ApprovalStage, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var stages []ApprovalStage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&stages); err != nil {
		var ids []string
		if json.Unmarshal(raw, &ids) != nil {
			return nil, routeError("Ожидается таблица этапов согласования")
		}
		if len(ids) > 0 {
			stages = []ApprovalStage{{ID: "legacy_stage", Title: "Согласование", Mode: "all", UserIDs: ids}}
		}
	}
	if len(stages) > 40 {
		return nil, routeError("Не больше 40 этапов согласования")
	}
	seen, total := map[string]bool{}, 0
	for _, stage := range stages {
		if !codePattern.MatchString(stage.ID) || seen[stage.ID] || strings.TrimSpace(stage.Title) == "" || len([]rune(stage.Title)) > 150 {
			return nil, routeError("Укажите уникальный код и название каждого этапа согласования")
		}
		seen[stage.ID] = true
		if stage.Mode != "all" && stage.Mode != "any" && stage.Mode != "sequential" {
			return nil, routeError("Неизвестный порядок согласования")
		}
		if len(stage.UserIDs) == 0 {
			return nil, routeError("Выберите участников каждого этапа согласования")
		}
		people := map[string]bool{}
		for _, id := range stage.UserIDs {
			if !goodID(id) || people[id] {
				return nil, routeError("Некорректный или повторяющийся участник согласования")
			}
			people[id] = true
			total++
		}
	}
	if total > 200 {
		return nil, routeError("Не больше 200 назначений в таблице согласования")
	}
	return stages, nil
}

// Expanded steps are persisted in the instance snapshot before any tasks are issued.
// They inherit the administrator's instructions, conditions and button snapshot.
func expandApprovalFlow(flow Flow, fields map[string]any) (Flow, error) {
	result := Flow{CompletionStatus: flow.CompletionStatus, Steps: []Step{}}
	used := map[string]bool{}
	for _, step := range flow.Steps {
		used[step.ID] = true
	}
	for index, template := range flow.Steps {
		var field string
		for _, source := range template.Assignees {
			if source.Kind == "approval_field" {
				field = source.ID
			}
		}
		if field == "" {
			result.Steps = append(result.Steps, template)
			continue
		}
		if len(template.Assignees) != 1 || len(template.AssigneeIDs) != 0 || template.ParallelGroup != "" || (template.Kind != "" && template.Kind != "approval") {
			return Flow{}, routeError("Таблицу согласования используйте отдельно в этапе «Согласование»")
		}
		stages, err := parseApproval(fields[field])
		if err != nil {
			return Flow{}, fmt.Errorf("Таблица «%s»: %w", field, err)
		}
		if len(stages) == 0 {
			return Flow{}, routeError("Заполните таблицу согласования «" + field + "» перед отправкой")
		}
		for i, stage := range stages {
			groups := [][]string{stage.UserIDs}
			if stage.Mode == "sequential" {
				groups = nil
				for _, id := range stage.UserIDs {
					groups = append(groups, []string{id})
				}
			}
			for j, ids := range groups {
				step := template
				base := fmt.Sprintf("ap_%d_%d_%d", index, i, j)
				step.ID = base
				for suffix := 1; used[step.ID]; suffix++ {
					step.ID = fmt.Sprintf("%s_%d", base, suffix)
				}
				used[step.ID] = true
				step.Title = stage.Title
				if len(groups) > 1 {
					step.Title = fmt.Sprintf("%s · %d/%d", stage.Title, j+1, len(groups))
				}
				step.Kind = "approval"
				step.Mode = stage.Mode
				if step.Mode == "sequential" {
					step.Mode = "all"
				}
				step.Assignees = nil
				step.AssigneeIDs = append([]string(nil), ids...)
				result.Steps = append(result.Steps, step)
			}
		}
	}
	if len(result.Steps) > 240 {
		return Flow{}, routeError("Маршрут после разворачивания таблиц содержит больше 240 этапов")
	}
	return result, nil
}
