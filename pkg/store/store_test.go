package store_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/naugustin/opamp-server/pkg/store"
)

func TestLocalStore(t *testing.T) {
	tmpDir := t.TempDir()

	// Dev mode
	devStore := store.NewLocalStore(tmpDir, store.ModeDevelopment)
	testYAML := []byte("receivers:\n  otlp:\n    endpoint: 0.0.0.0:4317\n")

	if err := devStore.WriteConfig("base", "", testYAML); err != nil {
		t.Fatalf("failed to write base config: %v", err)
	}

	base, err := devStore.GetBase()
	if err != nil {
		t.Fatalf("failed to get base config: %v", err)
	}
	if _, ok := base["receivers"]; !ok {
		t.Fatalf("receivers not found in read base config")
	}

	// Prod mode should reject writes
	prodStore := store.NewLocalStore(tmpDir, store.ModeProduction)
	err = prodStore.WriteConfig("roles", "database", testYAML)
	if !errors.Is(err, store.ErrProductionReadOnly) {
		t.Fatalf("expected ErrProductionReadOnly, got %v", err)
	}
}

func TestConfigMapStore(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir1, "base.yaml"), []byte("service: base\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir2, "database.yaml"), []byte("service: db\n"), 0644)

	cmStore := store.NewConfigMapStore([]string{dir1, dir2})

	base, err := cmStore.GetBase()
	if err != nil || base["service"] != "base" {
		t.Fatalf("failed to read base from configmap: %v", err)
	}

	role, err := cmStore.GetRole("database")
	if err != nil || role["service"] != "db" {
		t.Fatalf("failed to read role from configmap: %v", err)
	}

	if err := cmStore.WriteConfig("roles", "test", []byte("")); !errors.Is(err, store.ErrProductionReadOnly) {
		t.Fatalf("expected read only error on configmap write")
	}
}

func TestGiteaClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/ops/configs/raw/base.yaml" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("service: gitea\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := store.NewGiteaClient(store.GiteaConfig{
		BaseURL: server.URL,
		Owner:   "ops",
		Repo:    "configs",
		Branch:  "main",
	})

	gitStore := store.NewGitStore(client)
	base, err := gitStore.GetBase()
	if err != nil {
		t.Fatalf("failed to get base from gitea client: %v", err)
	}
	if base["service"] != "gitea" {
		t.Fatalf("expected service gitea, got %v", base["service"])
	}

	editURL := client.GetEditURL("roles/database.yaml")
	expected := server.URL + "/ops/configs/_edit/main/roles/database.yaml"
	if editURL != expected {
		t.Fatalf("expected edit url %s, got %s", expected, editURL)
	}
}

func TestRedisInMemoryStore(t *testing.T) {
	rStore := store.NewInMemoryOverrideStore()

	cfg := map[string]interface{}{"timeout": "1s"}
	meta := store.OverrideMetadata{
		Target: "prod-db-01",
		Author: "alice",
		Reason: "emergency",
	}

	// Test Set and Get
	if err := rStore.SetOverride("prod-db-01", cfg, 1*time.Hour, meta); err != nil {
		t.Fatalf("set override failed: %v", err)
	}

	res, err := rStore.GetOverride("prod-db-01")
	if err != nil || res["timeout"] != "1s" {
		t.Fatalf("get override failed: %v", err)
	}

	// Test List
	list, err := rStore.ListOverrides()
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 override in list, got %d", len(list))
	}

	// Test Expiry
	_ = rStore.SetOverride("short-lived", cfg, 2*time.Millisecond, meta)
	time.Sleep(10 * time.Millisecond)
	expiredRes, _ := rStore.GetOverride("short-lived")
	if expiredRes != nil {
		t.Fatalf("expected override to expire, but got %v", expiredRes)
	}

	// Test Delete
	_ = rStore.DeleteOverride("prod-db-01")
	deletedRes, _ := rStore.GetOverride("prod-db-01")
	if deletedRes != nil {
		t.Fatalf("expected override to be deleted")
	}
}
