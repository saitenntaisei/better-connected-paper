package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	r := NewRouter(Deps{})
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %v, want \"ok\"", body["status"])
	}
	if _, ok := body["time"]; !ok {
		t.Error("expected time field in response")
	}
	persistence, ok := body["persistence"].(map[string]any)
	if !ok {
		t.Fatalf("expected persistence object, got %T: %v", body["persistence"], body["persistence"])
	}
	if persistence["enabled"] != false {
		t.Errorf("persistence.enabled = %v, want false when DB is nil", persistence["enabled"])
	}
	if persistence["healthy"] != false {
		t.Errorf("persistence.healthy = %v, want false when DB is nil", persistence["healthy"])
	}
}

func TestNotFound(t *testing.T) {
	r := NewRouter(Deps{})
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
