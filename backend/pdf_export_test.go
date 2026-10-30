package main

import (
	"net/http/httptest"
	"testing"
)

func TestPDFFileVersionBinding(t *testing.T) {
	payload := []byte(`{"files":[{"id":"version-1","sha256":"abc"}]}`)
	if !coversFile(payload, "version-1", "abc") {
		t.Fatal("covered file rejected")
	}
	for _, p := range [][2]string{{"version-2", "abc"}, {"version-1", "changed"}} {
		if coversFile(payload, p[0], p[1]) {
			t.Fatal("uncovered version accepted")
		}
	}
	if coversFile([]byte(`invalid`), "version-1", "abc") {
		t.Fatal("malformed manifest accepted")
	}
}
func TestPDFPublicAndPrivateBoundaries(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/api/v1/files/11111111-1111-4111-8111-111111111111/signed-pdf", "/api/v1/documents/11111111-1111-4111-8111-111111111111/signed-pdf"} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("unauthenticated download: %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/public/verify/guess", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
