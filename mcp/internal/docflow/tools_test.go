package docflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, c *Client) (context.Context, *mcp.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	a, b := mcp.NewInMemoryTransports()
	server, err := NewServer(c).Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "docflow-test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return ctx, session
}
func invoke(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args any) (any, bool) {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		encoded, _ := json.Marshal(result.Content)
		return string(encoded), true
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		t.Fatal("missing structured content")
	}
	return object["data"], false
}
func TestMCPToolsAndReadOnly(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/v1/me":
			w.Write([]byte(`{"id":"` + id + `","role":"employee"}`))
		case "/api/v1/users":
			w.Write([]byte(`[{"id":"` + id + `","full_name":"Tester","active":true,"email":"test@local","signing_iin":"SHOULD_NOT_LEAK"}]`))
		case "/api/v1/documents":
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["type_id"] != id || body["title"] != "Test" {
				t.Error("invalid create payload")
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"id":"` + id + `"}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	c := testClient(t, api)
	ctx, session := connect(t, c)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 25 {
		t.Fatalf("got %d tools; expected 25", len(tools.Tools))
	}
	if _, failed := invoke(t, ctx, session, "docflow_get_me", map[string]any{}); failed {
		t.Fatal("profile failed")
	}
	users, failed := invoke(t, ctx, session, "docflow_list_users", map[string]any{"query": "test"})
	if failed {
		t.Fatal("directory failed")
	}
	encoded, _ := json.Marshal(users)
	if string(encoded) == "null" {
		t.Fatal("missing users")
	}
	var page map[string]any
	_ = json.Unmarshal(encoded, &page)
	user := page["items"].([]any)[0].(map[string]any)
	if _, ok := user["signing_iin"]; ok {
		t.Fatal("IIN leaked")
	}
	if _, failed := invoke(t, ctx, session, "docflow_create_document", map[string]any{"type_id": id, "title": "Test"}); failed {
		t.Fatal("create failed")
	}
	before := calls
	if _, failed := invoke(t, ctx, session, "docflow_get_document", map[string]any{"id": "../admin"}); !failed {
		t.Fatal("invalid ID accepted")
	}
	if calls != before {
		t.Fatal("invalid ID reached API")
	}
	ro := testClient(t, api)
	ro.ReadOnly = true
	rctx, rs := connect(t, ro)
	list, err := rs.ListTools(rctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 19 {
		t.Fatalf("got %d read-only tools", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if !tool.Annotations.ReadOnlyHint {
			t.Fatal("mutation in read-only list")
		}
	}
	before = calls
	result, err := rs.CallTool(rctx, &mcp.CallToolParams{Name: "docflow_create_document", Arguments: map[string]any{"type_id": id, "title": "No"}})
	if err == nil && !result.IsError {
		t.Fatal("hidden mutation available")
	}
	if calls != before {
		t.Fatal("read-only mutation reached API")
	}
}

func TestStdioBinary(t *testing.T) {
	binary := os.Getenv("DOCFLOW_MCP_BINARY")
	if binary == "" {
		t.Skip("DOCFLOW_MCP_BINARY not set")
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer stdio-test-token" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"id":"stdio-user","role":"employee"}`))
	}))
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.Command(binary)
	command.Env = append(os.Environ(), "DOCFLOW_CONFIG_FILE=", "DOCFLOW_EMAIL=", "DOCFLOW_PASSWORD=", "DOCFLOW_REFRESH_TOKEN=", "DOCFLOW_ACCESS_TOKEN=stdio-test-token", "DOCFLOW_API_URL="+api.URL, "DOCFLOW_MCP_READ_ONLY=true")
	session, err := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, failed := invoke(t, ctx, session, "docflow_get_me", map[string]any{}); failed {
		t.Fatal("stdio tool failed")
	}
}
