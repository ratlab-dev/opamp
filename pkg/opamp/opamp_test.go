package opamp_test

import (
	"testing"

	"github.com/naugustin/opamp-server/pkg/opamp"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestAgentRegistry(t *testing.T) {
	registry := opamp.NewAgentRegistry(nil, "test-node")

	uid := []byte("instance-uuid-01")
	desc := &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key:   "host.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "prod-db-01"}},
			},
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key:   "deployment.environment",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "prod"}},
			},
			{
				Key:   "service.role",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "database"}},
			},
			{
				Key:   "k8s.cluster.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "us-east-1"}},
			},
		},
	}

	remoteStatus := &protobufs.RemoteConfigStatus{
		Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		LastRemoteConfigHash: []byte("initial-hash"),
	}

	info := registry.RegisterOrUpdate(uid, "10.0.0.1:45678", desc, remoteStatus, nil)
	if info == nil {
		t.Fatalf("expected registered agent info")
	}

	if info.Hostname != "prod-db-01" {
		t.Fatalf("expected hostname prod-db-01, got %s", info.Hostname)
	}
	if info.Environment != "prod" {
		t.Fatalf("expected environment prod, got %s", info.Environment)
	}
	if info.Role != "database" {
		t.Fatalf("expected role database, got %s", info.Role)
	}
	if info.Cluster != "us-east-1" {
		t.Fatalf("expected cluster us-east-1, got %s", info.Cluster)
	}
	if info.Status != "IN_SYNC" {
		t.Fatalf("expected status IN_SYNC, got %s", info.Status)
	}

	ctx := registry.ToRenderContext(info)
	if ctx.Hostname != "prod-db-01" || ctx.Environment != "prod" || ctx.Role != "database" || ctx.Cluster != "us-east-1" {
		t.Fatalf("render context mismatched: %+v", ctx)
	}

	list := registry.ListAgents()
	if len(list) != 1 {
		t.Fatalf("expected 1 agent in list, got %d", len(list))
	}
}

func TestEffectiveConfigCapture(t *testing.T) {
	registry := opamp.NewAgentRegistry(nil, "node-1")
	uid := []byte("agent-with-config")

	effectiveCfg := &protobufs.EffectiveConfig{
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigObject{
				"": {
					Body:        []byte("service:\n  telemetry:\n    logs:\n      level: debug\n"),
					ContentType: "text/yaml",
				},
			},
		},
	}

	info := registry.RegisterOrUpdate(uid, "127.0.0.1:4000", nil, nil, effectiveCfg)
	if info == nil {
		t.Fatalf("expected registered info")
	}

	if info.ReportedConfigYAML != "service:\n  telemetry:\n    logs:\n      level: debug\n" {
		t.Fatalf("reported config YAML not captured correctly, got: %s", info.ReportedConfigYAML)
	}
}

func TestEventBroker(t *testing.T) {
	broker := opamp.NewEventBroker()
	done := make(chan bool, 1)

	// Simulate client
	clientChan := make(chan []byte, 1)
	broker.Broadcast("test_event", map[string]string{"key": "value"})

	// If no clients registered, doesn't panic
	done <- true
	select {
	case <-done:
	case <-clientChan:
	}
}
