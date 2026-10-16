package main

import (
	"testing"
)

func TestValidateFlow(t *testing.T) {
	good := Flow{Steps: []Step{{ID: "review", Title: "Review", Mode: "all", AssigneeIDs: []string{"11111111-1111-4111-8111-111111111111"}}, {ID: "final", Title: "Final", Mode: "any", AssigneeIDs: []string{"22222222-2222-4222-8222-222222222222"}}}}
	if e := validateFlow(good); e != "" {
		t.Fatalf("valid route rejected: %s", e)
	}
	bad := good
	bad.Steps = append([]Step{}, good.Steps...)
	bad.Steps[1].ID = "review"
	if e := validateFlow(bad); e == "" {
		t.Fatal("duplicate step ID accepted")
	}
}
func TestConditions(t *testing.T) {
	doc := map[string]any{"amount": float64(125000), "kind": "contract", "flag": true}
	cases := []struct {
		cond Condition
		want bool
	}{
		{Condition{Field: "amount", Operator: "gt", Value: float64(100000)}, true},
		{Condition{Field: "amount", Operator: "lt", Value: float64(100000)}, false},
		{Condition{Field: "kind", Operator: "eq", Value: "contract"}, true},
		{Condition{Field: "kind", Operator: "contains", Value: "TRACT"}, true},
		{Condition{Field: "missing", Operator: "neq", Value: "x"}, false},
	}
	for _, c := range cases {
		if got := conditionMatches(&c.cond, doc); got != c.want {
			t.Errorf("%+v: got %v want %v", c.cond, got, c.want)
		}
	}
}
func TestFieldValidation(t *testing.T) {
	defs := []FieldDef{{Key: "amount", Label: "Сумма", Type: "number", Required: true}, {Key: "signable", Label: "Согласование", Type: "boolean"}, {Key: "kind", Label: "Вид", Type: "select", Options: []string{"A", "B"}}}
	if e := validateValues(defs, map[string]any{"amount": float64(100), "signable": true, "kind": "B"}); e != "" {
		t.Fatal(e)
	}
	for _, values := range []map[string]any{{"amount": "abc"}, {"amount": float64(1), "other": "x"}, {"amount": float64(1), "kind": "C"}} {
		if e := validateValues(defs, values); e == "" {
			t.Errorf("invalid fields accepted: %+v", values)
		}
	}
}
func TestSignedAccessToken(t *testing.T) {
	srv := Server{secret: []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}
	id := "11111111-1111-4111-8111-111111111111"
	tok := srv.makeAccess(id)
	got, err := srv.verifyAccess(tok)
	if err != nil || got != id {
		t.Fatalf("bad token: %v / %s", err, got)
	}
	tok = tok[:len(tok)-2] + "aa"
	if _, err = srv.verifyAccess(tok); err == nil {
		t.Fatal("tampered token accepted")
	}
}
