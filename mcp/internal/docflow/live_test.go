package docflow

import (
	"context"
	"os"
	"testing"
	"time"
)

// The runner starts a separate API process with a disposable docflow_test_ DB.
// Never point this test at the user's application database.
func TestLiveMCPWorkflow(t *testing.T) {
	base := os.Getenv("DOCFLOW_MCP_TEST_API_URL")
	if base == "" {
		t.Skip("isolated API not configured")
	}
	if os.Getenv("DOCFLOW_MCP_TEST_ALLOW_WRITES") != "isolated-database" {
		t.Fatal("explicit isolated-database guard required")
	}
	c, err := NewClient(Config{APIURL: base, Email: os.Getenv("DOCFLOW_MCP_TEST_EMAIL"), Password: os.Getenv("DOCFLOW_MCP_TEST_PASSWORD")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	ctx, session := connect(t, c)
	call := func(name string, args any) any {
		t.Helper()
		data, failed := invoke(t, ctx, session, name, args)
		if failed {
			t.Fatalf("%s failed: %v", name, data)
		}
		return data
	}
	fixture := func(method, path string, body any) map[string]any {
		t.Helper()
		data, err := c.Do(ctx, method, path, body)
		if err != nil {
			t.Fatal(err)
		}
		return data.(map[string]any)
	}
	me := call("docflow_get_me", map[string]any{}).(map[string]any)
	actor := me["id"].(string)
	if me["role"] != "admin" {
		t.Fatal("fixture setup requires test administrator")
	}
	employeePassword := "test-only-password-" + time.Now().Format("150405000")
	employee := fixture("POST", "/admin/users", map[string]any{"email": "mcp-employee@test.local", "full_name": "MCP Test Employee", "password": employeePassword, "role": "employee"})["id"].(string)
	defs := []any{map[string]any{"key": "reviewers", "label": "Reviewers", "type": "approval", "required": true}, map[string]any{"key": "note", "label": "Note", "type": "string", "required": false}}
	typ := fixture("POST", "/admin/document-types", map[string]any{"code": "mcp_test_kind", "name": "MCP Test", "fields": defs})["id"].(string)
	fixture("POST", "/admin/workflows", map[string]any{"workflow_key": "mcp_test_route", "name": "MCP Test Route", "doc_type_id": typ, "definition": map[string]any{"completion_status": "completed", "steps": []any{
		map[string]any{"id": "approve_table", "title": "Review", "kind": "approval", "mode": "all", "assignee_ids": []string{}, "assignees": []any{map[string]string{"kind": "approval_field", "id": "reviewers"}}, "button_codes": []string{"approve", "return", "reject"}},
		map[string]any{"id": "execute_task", "title": "Execute", "kind": "task", "mode": "all", "assignee_ids": []string{}, "assignees": []any{map[string]string{"kind": "author"}}, "button_codes": []string{"complete"}},
	}}})
	fixture("PUT", "/admin/document-types/"+typ, map[string]any{"name": "MCP Test", "fields": defs, "default_workflow_key": "mcp_test_route"})
	call("docflow_list_document_types", map[string]any{"query": "MCP Test"})
	id := call("docflow_create_document", map[string]any{"type_id": typ, "title": "Created through MCP", "fields": map[string]any{"note": "preserve me", "reviewers": []any{map[string]any{"id": "review_stage", "title": "Approval", "mode": "all", "user_ids": []string{actor}}}}}).(map[string]any)["id"].(string)
	card := call("docflow_get_document", map[string]any{"id": id}).(map[string]any)
	timestamp := card["updated_at"]
	call("docflow_update_document", map[string]any{"id": id, "expected_updated_at": timestamp, "summary": "Updated through MCP"})
	if _, failed := invoke(t, ctx, session, "docflow_update_document", map[string]any{"id": id, "expected_updated_at": timestamp, "summary": "Stale"}); !failed {
		t.Fatal("stale edit accepted")
	}
	card = call("docflow_get_document", map[string]any{"id": id}).(map[string]any)
	if card["fields"].(map[string]any)["note"] != "preserve me" {
		t.Fatal("partial update lost fields")
	}
	worker, err := NewClient(Config{APIURL: base, Email: "mcp-employee@test.local", Password: employeePassword})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close(context.Background())
	wctx, wsession := connect(t, worker)
	if _, failed := invoke(t, wctx, wsession, "docflow_get_document", map[string]any{"id": id}); !failed {
		t.Fatal("unrelated employee read the document")
	}
	call("docflow_start_workflow", map[string]any{"id": id})
	route := call("docflow_get_document_route", map[string]any{"id": id}).([]any)
	approvalID := route[0].(map[string]any)["id"].(string)
	call("docflow_decide_task", map[string]any{"task_id": approvalID, "action_code": "approve"})
	route = call("docflow_get_document_route", map[string]any{"id": id}).([]any)
	var taskID string
	for _, row := range route {
		task := row.(map[string]any)
		if task["status"] == "pending" {
			taskID = task["id"].(string)
		}
	}
	if taskID == "" {
		t.Fatal("execution task missing")
	}
	delegated := call("docflow_delegate_task", map[string]any{"task_id": taskID, "assignee_id": employee, "comment": "Test delegation"}).(map[string]any)["id"].(string)
	if _, failed := invoke(t, ctx, session, "docflow_decide_task", map[string]any{"task_id": delegated, "action_code": "complete"}); !failed {
		t.Fatal("administrator completed somebody else's task")
	}
	if _, failed := invoke(t, wctx, wsession, "docflow_list_tasks", map[string]any{}); failed {
		t.Fatal("employee tasks unavailable")
	}
	if _, failed := invoke(t, wctx, wsession, "docflow_decide_task", map[string]any{"task_id": delegated, "action_code": "complete", "comment": "Done"}); failed {
		t.Fatal("delegate could not complete task")
	}
	card = call("docflow_get_document", map[string]any{"id": id}).(map[string]any)
	if card["status"] != "completed" {
		t.Fatalf("route did not finish: %v", card["status"])
	}
	history := call("docflow_get_document_history", map[string]any{"id": id}).([]any)
	found := false
	for _, row := range history {
		if row.(map[string]any)["kind"] == "workflow.task_delegated" {
			found = true
		}
	}
	if !found {
		t.Fatal("delegation was not audited")
	}
}
