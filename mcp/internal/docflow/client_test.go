package docflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(Config{APIURL: server.URL, AccessToken: "test-access"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientSerializesRefresh(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			refreshes.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 900})
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-access" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "expired"})
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()
	c := testClient(t, server)
	c.refresh = "old-refresh"
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := c.Do(context.Background(), "GET", "/me", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refresh rotated %d times", refreshes.Load())
	}
}

func TestClientNoRedirectNoMutationRetry(t *testing.T) {
	var leaked, calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == "POST" {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	c := testClient(t, server)
	if _, err := c.Do(context.Background(), "GET", "/me", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("credentials followed redirect")
	}
	before := calls.Load()
	if _, err := c.Do(context.Background(), "POST", "/documents", map[string]any{}); err == nil {
		t.Fatal("mutation error lost")
	}
	if calls.Load() != before+1 {
		t.Fatal("mutation retried")
	}
	c.ReadOnly = true
	before = calls.Load()
	if _, err := c.Do(context.Background(), "POST", "/documents", nil); err == nil {
		t.Fatal("read-only bypass")
	}
	if calls.Load() != before {
		t.Fatal("read-only contacted mutation endpoint")
	}
}

func TestClientURLAndCancellation(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?secret=x", "file:///tmp", "https://example.com/other"} {
		if _, err := NewClient(Config{APIURL: base, AccessToken: "token"}); err == nil {
			t.Fatalf("accepted URL %s", base)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	c := testClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Do(ctx, "GET", "/me", nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestPartialUpdatePreservesFieldsAndRejectsStaleVersion(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	timestamp := "2026-11-01T12:00:00.123456+05:00"
	var patched map[string]any
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(map[string]any{"id": id, "type_id": id, "title": "Keep title", "summary": "Old", "registration_number": "REG-1", "assignee_id": "", "department_id": "", "due_at": nil, "updated_at": timestamp, "fields": map[string]any{"keep": "unchanged", "remove": "old"}})
			return
		}
		calls++
		_ = json.NewDecoder(r.Body).Decode(&patched)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	c := testClient(t, server)
	summary := "New"
	if _, err := c.updateDocument(context.Background(), updateArgs{ID: id, ExpectedUpdatedAt: timestamp, Summary: &summary, Fields: map[string]any{"remove": nil, "new": "value"}}); err != nil {
		t.Fatal(err)
	}
	fields := patched["fields"].(map[string]any)
	if patched["title"] != "Keep title" || patched["registration_number"] != "REG-1" || fields["keep"] != "unchanged" || fields["new"] != "value" || fields["remove"] != nil || patched["expected_updated_at"] != timestamp {
		t.Fatalf("unsafe merge: %v", patched)
	}
	_, err := c.updateDocument(context.Background(), updateArgs{ID: id, ExpectedUpdatedAt: "2026-10-01T00:00:00Z", Summary: &summary})
	if err == nil || calls != 1 {
		t.Fatal("stale update submitted")
	}
}

func TestSecretsRedactedAndSafePaging(t *testing.T) {
	c := &Client{password: "secret-pass", access: "secret-access", refresh: "secret-refresh"}
	if strings.Contains(c.redact("secret-pass secret-access secret-refresh"), "secret-") {
		t.Fatal("secret not redacted")
	}
	result, err := page([]any{1, 2}, int(^uint(0)>>1), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("paging should be empty")
	}
}

func TestQueryDotsAreData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "draft..review" {
			t.Error("query changed")
		}
		w.Write([]byte(`[]`))
	}))
	defer server.Close()
	c := testClient(t, server)
	if _, err := c.Do(context.Background(), "GET", "/documents?q=draft..review", nil); err != nil {
		t.Fatal(err)
	}
}
