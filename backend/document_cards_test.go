package main

import "testing"

func TestCardConfiguration(t *testing.T) {
	card := []CardField{
		{Key: "title", Label: "Заголовок", Visible: true, Required: true},
		{Key: "summary", Label: "Описание", Visible: false},
		{Key: "registration_number", Label: "Номер", Visible: true},
		{Key: "assignee_id", Label: "Исполнитель", Visible: true},
		{Key: "department_id", Label: "Отдел", Visible: true},
		{Key: "due_at", Label: "Срок", Visible: true},
	}
	if msg := checkCardFields(card); msg != "" {
		t.Fatal(msg)
	}
	card[0].Visible = false
	if msg := checkCardFields(card); msg == "" {
		t.Fatal("hidden document title accepted")
	}
	card[0].Visible = true
	card[1].Required = true
	if msg := checkCardFields(card); msg == "" {
		t.Fatal("hidden required field accepted")
	}
}

func TestMultipleUserFieldValidation(t *testing.T) {
	defs := []FieldDef{{Key: "reviewers", Label: "Ознакомить", Type: "users", Required: true}}
	if msg := checkFields(defs); msg != "" {
		t.Fatal(msg)
	}
	first := "11111111-1111-4111-8111-111111111111"
	second := "22222222-2222-4222-8222-222222222222"
	for _, fields := range []map[string]any{{}, {"reviewers": []any{}}, {"reviewers": []any{first, first}}, {"reviewers": []any{"bad"}}} {
		if msg := validateValues(defs, fields); msg == "" {
			t.Fatalf("invalid users accepted: %#v", fields)
		}
	}
	if msg := validateValues(defs, map[string]any{"reviewers": []any{first, second}}); msg != "" {
		t.Fatal(msg)
	}
}
