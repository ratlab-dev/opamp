package audit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/naugustin/opamp-server/pkg/audit"
)

func TestAuditDispatcher(t *testing.T) {
	var receivedEvent string
	var receivedSig string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedEvent = r.Header.Get("X-OpAMP-Audit-Event")
		receivedSig = r.Header.Get("X-OpAMP-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret := "test-secret"
	dispatcher := audit.NewDispatcher(audit.WebhookConfig{
		URL:     srv.URL,
		Secret:  secret,
		Timeout: 2 * time.Second,
	})

	event := audit.NewEvent(
		audit.EventTemporaryOverrideCreated,
		"test_action",
		audit.Actor{Identity: "operator"},
		audit.Target{Hostname: "host-1"},
		audit.EventDetails{Reason: "test"},
	)

	err := dispatcher.Publish(context.Background(), event)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if receivedEvent != string(audit.EventTemporaryOverrideCreated) {
		t.Fatalf("expected event %s, got %s", audit.EventTemporaryOverrideCreated, receivedEvent)
	}
	if receivedSig == "" {
		t.Fatalf("expected non-empty HMAC signature header")
	}

	recent := dispatcher.GetRecentEvents(10)
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent event in memory buffer, got %d", len(recent))
	}
}

func TestComputeSignature(t *testing.T) {
	secret := "mysecret"
	payload := []byte(`{"test":"data"}`)
	sig := audit.ComputeSignature(payload, secret)
	if sig == "" {
		t.Fatalf("signature should not be empty")
	}

	// Verify deterministic signature
	sig2 := audit.ComputeSignature(payload, secret)
	if sig != sig2 {
		t.Fatalf("signatures should be identical for same payload and secret")
	}
}
