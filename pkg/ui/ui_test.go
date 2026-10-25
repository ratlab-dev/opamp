package ui_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/naugustin/opamp-server/pkg/audit"
	"github.com/naugustin/opamp-server/pkg/opamp"
	"github.com/naugustin/opamp-server/pkg/store"
	"github.com/naugustin/opamp-server/pkg/ui"
)

func setupTestServer(t *testing.T, mode store.Mode) (*ui.Handler, *store.CombinedStore) {
	tmpDir := t.TempDir()
	baseStore := store.NewLocalStore(tmpDir, mode)
	_ = baseStore.WriteConfig("base", "", []byte("service:\n  pipelines:\n    traces:\n      receivers: [otlp]\n"))

	redisStore := store.NewInMemoryOverrideStore()
	combinedStore := store.NewCombinedStore(baseStore, redisStore, mode, nil)

	auditPub := audit.NewDispatcher(audit.WebhookConfig{})
	opampSrv, err := opamp.NewServer(combinedStore, auditPub, redisStore, "test-node")
	if err != nil {
		t.Fatalf("failed to create opamp server: %v", err)
	}

	handler := ui.NewHandler(combinedStore, opampSrv, auditPub)
	return handler, combinedStore
}

func TestUIEndpoints(t *testing.T) {
	handler, _ := setupTestServer(t, store.ModeDevelopment)

	// Test GET / (HTML index)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for index, got %d", rec.Code)
	}

	// Test GET /api/status
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for status, got %d", rec.Code)
	}
	var status map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if status["mode"] != "development" {
		t.Fatalf("expected mode development, got %v", status["mode"])
	}

	// Test POST /api/render
	renderBody := []byte(`{"environment":"prod","hostname":"node-1"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/render", bytes.NewReader(renderBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for render, got %d: %s", rec.Code, rec.Body.String())
	}
	var renderResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &renderResp)
	if renderResp["hash"] == "" || renderResp["yaml"] == "" {
		t.Fatalf("expected hash and yaml in render response")
	}

	// Test POST /api/overrides (create temporary override)
	overrideBody := []byte(`{
		"target": "node-1",
		"duration": "1h",
		"author": "tester",
		"reason": "debugging",
		"config_yaml": "service:\n  telemetry:\n    logs:\n      level: debug\n"
	}`)
	req = httptest.NewRequest(http.MethodPost, "/api/overrides", bytes.NewReader(overrideBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for create override, got %d: %s", rec.Code, rec.Body.String())
	}

	// Test GET /api/overrides
	req = httptest.NewRequest(http.MethodGet, "/api/overrides", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for get overrides, got %d", rec.Code)
	}
	var overrides []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &overrides)
	if len(overrides) != 1 || overrides[0]["target"] != "node-1" {
		t.Fatalf("expected 1 override for node-1, got %v", overrides)
	}

	// Test DELETE /api/overrides
	req = httptest.NewRequest(http.MethodDelete, "/api/overrides?target=node-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for delete override, got %d", rec.Code)
	}

	// Test POST /api/overrides/preview (host scope preview)
	previewBody := []byte(`{
		"target": "host:node-1",
		"config_yaml": "service:\n  telemetry:\n    logs:\n      level: debug\n"
	}`)
	req = httptest.NewRequest(http.MethodPost, "/api/overrides/preview", bytes.NewReader(previewBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for preview, got %d: %s", rec.Code, rec.Body.String())
	}
	var previewResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &previewResp)
	if previewResp["target"] != "host:node-1" {
		t.Fatalf("expected target host:node-1, got %v", previewResp["target"])
	}
	if previewResp["effective_proposed_yaml"] == "" || previewResp["layer_proposed_yaml"] == "" {
		t.Fatalf("expected non-empty layer and effective proposed YAML")
	}

	// Test POST /api/overrides/preview (role scope preview)
	rolePreviewBody := []byte(`{
		"target": "role:database",
		"config_yaml": "processors:\n  batch:\n    timeout: 2s\n"
	}`)
	req = httptest.NewRequest(http.MethodPost, "/api/overrides/preview", bytes.NewReader(rolePreviewBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for role preview, got %d: %s", rec.Code, rec.Body.String())
	}
	var rolePreviewResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &rolePreviewResp)
	if rolePreviewResp["scope_type"] != "role" || rolePreviewResp["scope_level_name"] != "Role (database)" {
		t.Fatalf("expected scope_type role, got %v", rolePreviewResp)
	}

	// Test GET /api/store/tree
	req = httptest.NewRequest(http.MethodGet, "/api/store/tree", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for store tree, got %d", rec.Code)
	}
	var treeResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &treeResp)
	if treeResp["tree"] == nil {
		t.Fatalf("expected tree array in response")
	}

	// Test GET /api/store/file
	req = httptest.NewRequest(http.MethodGet, "/api/store/file?path=base.yaml", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for store file, got %d: %s", rec.Code, rec.Body.String())
	}
	var fileResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &fileResp)
	if fileResp["path"] != "base.yaml" || fileResp["content_yaml"] == "" {
		t.Fatalf("expected base.yaml content, got %v", fileResp)
	}

	// Test GET /api/audit
	req = httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for get audit, got %d", rec.Code)
	}
}

func TestProductionModeProtection(t *testing.T) {
	handler, _ := setupTestServer(t, store.ModeProduction)

	// Test POST /api/config/save in production mode should be 403 Forbidden
	saveBody := []byte(`{"layer":"roles","name":"db","config_yaml":"service: test\n"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/config/save", bytes.NewReader(saveBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden in production mode, got %d", rec.Code)
	}
}
