package main

import (
	"encoding/json"
	"testing"
)

func TestApprovalValidationAndExpansion(t *testing.T) {
	a, b := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	stages := []ApprovalStage{{ID: "first_stage", Title: "Юристы", Mode: "sequential", UserIDs: []string{a, b}}, {ID: "second_stage", Title: "Руководители", Mode: "any", UserIDs: []string{a, b}}}
	flow := Flow{CompletionStatus: "completed", Steps: []Step{{ID: "approval_table", Title: "Согласование", Kind: "approval", Mode: "all", Assignees: []AssigneeSource{{Kind: "approval_field", ID: "reviewers"}}, Actions: legacyButtons()}}}
	if msg := validateFlow(flow); msg != "" {
		t.Fatal(msg)
	}
	expanded, err := expandApprovalFlow(flow, map[string]any{"reviewers": stages})
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.Steps) != 3 || expanded.Steps[0].AssigneeIDs[0] != a || expanded.Steps[1].AssigneeIDs[0] != b || expanded.Steps[2].Mode != "any" || len(expanded.Steps[2].AssigneeIDs) != 2 || expanded.CompletionStatus != "completed" {
		t.Fatalf("incorrect execution plan: %+v", expanded)
	}
	if len(expanded.Steps[0].Actions) != 3 || flow.Steps[0].Assignees[0].Kind != "approval_field" {
		t.Fatal("button inheritance or original template changed")
	}
	parallel := append([]ApprovalStage(nil), stages...)
	parallel[0].Mode = "all"
	result, err := expandApprovalFlow(flow, map[string]any{"reviewers": parallel})
	if err != nil || len(result.Steps) != 2 || len(result.Steps[0].AssigneeIDs) != 2 {
		t.Fatalf("parallel expansion: %+v %v", result, err)
	}
	if _, err = expandApprovalFlow(flow, map[string]any{}); err == nil {
		t.Fatal("empty table accepted")
	}
	legacy, err := parseApproval([]string{a, b})
	if err != nil || len(legacy) != 1 || legacy[0].Mode != "all" {
		t.Fatal("legacy users were not preserved")
	}
	bad := []any{
		[]ApprovalStage{{ID: "stage_one", Title: "Invalid", Mode: "all", UserIDs: []string{a, a}}},
		[]ApprovalStage{{ID: "stage_one", Title: "Invalid", Mode: "all", UserIDs: []string{"bad-id"}}},
		[]ApprovalStage{{ID: "stage_one", Title: "Invalid", Mode: "unknown", UserIDs: []string{a}}},
		[]ApprovalStage{{ID: "stage_one", Title: "Invalid", Mode: "all"}},
		map[string]any{"stages": "bad"},
	}
	for _, value := range bad {
		if _, err := parseApproval(value); err == nil {
			t.Fatalf("accepted invalid value: %+v", value)
		}
	}
	defs := []FieldDef{{Key: "reviewers", Label: "Согласующие", Type: "approval", Required: true}}
	raw, _ := json.Marshal(stages)
	var value any
	_ = json.Unmarshal(raw, &value)
	if msg := validateValues(defs, map[string]any{"reviewers": value}); msg != "" {
		t.Fatal(msg)
	}
	if msg := validateValues(defs, map[string]any{"reviewers": []any{}}); msg == "" {
		t.Fatal("required empty table accepted")
	}
	flow.Steps[0].ParallelGroup = "parallel_group"
	if msg := validateFlow(flow); msg == "" {
		t.Fatal("ambiguous parallel table accepted")
	}
}
