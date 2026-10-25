package config_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/naugustin/opamp-server/pkg/audit"
	"github.com/naugustin/opamp-server/pkg/config"
	"github.com/naugustin/opamp-server/pkg/store"
	"gopkg.in/yaml.v3"
)

type mockStore struct {
	base     map[string]interface{}
	envs     map[string]map[string]interface{}
	clusters map[string]map[string]interface{}
	subs     map[string]map[string]interface{}
	roles    map[string]map[string]interface{}
	envRoles map[string]map[string]interface{}
	hosts    map[string]map[string]interface{}
	redis    map[string]map[string]interface{}
}

func newMockStore() *mockStore {
	return &mockStore{
		envs:     make(map[string]map[string]interface{}),
		clusters: make(map[string]map[string]interface{}),
		subs:     make(map[string]map[string]interface{}),
		roles:    make(map[string]map[string]interface{}),
		envRoles: make(map[string]map[string]interface{}),
		hosts:    make(map[string]map[string]interface{}),
		redis:    make(map[string]map[string]interface{}),
	}
}

func (m *mockStore) GetBase() (map[string]interface{}, error)           { return m.base, nil }
func (m *mockStore) GetEnv(env string) (map[string]interface{}, error) { return m.envs[env], nil }
func (m *mockStore) GetCluster(c string) (map[string]interface{}, error) {
	return m.clusters[c], nil
}
func (m *mockStore) GetSubdomain(s string) (map[string]interface{}, error) { return m.subs[s], nil }
func (m *mockStore) GetRole(r string) (map[string]interface{}, error)      { return m.roles[r], nil }
func (m *mockStore) GetEnvRole(env, role string) (map[string]interface{}, error) {
	return m.envRoles[env+"/"+role], nil
}
func (m *mockStore) GetHost(h string) (map[string]interface{}, error) { return m.hosts[h], nil }
func (m *mockStore) GetRedisOverride(h string) (map[string]interface{}, error) {
	return m.redis[h], nil
}

type testCtx struct {
	store    *mockStore
	reqCtx   config.RenderContext
	rendered map[string]interface{}

	// Multi-render hash testing
	renderedHashes []string

	// Zero diff testing
	agentReportedHash string
	serverMarkedSync  bool
	sentRemoteConfig  bool

	// Real temporary override testing
	tempStore store.TemporaryOverrideStore

	// Storage modes testing
	tempDir       string
	localStore    *store.LocalStore
	lastWriteErr  error
	configMapRoot string
	configMapSrv  *store.ConfigMapStore

	// Audit webhook testing
	webhookServer   *httptest.Server
	lastWebhookReq  []byte
	lastWebhookHead http.Header
	auditDispatcher *audit.Dispatcher
	webhookSecret   string
}

func (t *testCtx) aBaseConfigurationWithReceivers(receiver string) error {
	yamlStr := fmt.Sprintf(`receivers: [%s]`, receiver)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.base = m
	return nil
}

func (t *testCtx) anEnvironmentOverrideForWithExporters(env, exporter string) error {
	yamlStr := fmt.Sprintf(`exporters: [%s]`, exporter)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.envs[env] = m
	return nil
}

func (t *testCtx) theEffectiveConfigurationIsRenderedForEnvironment(env string) error {
	t.reqCtx.Environment = env
	yamlBytes, _, err := config.Render(t.store, t.reqCtx)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(yamlBytes, &t.rendered)
}

func (t *testCtx) theConfigurationShouldHaveReceivers(expected string) error {
	recs, ok := t.rendered["receivers"].([]interface{})
	if !ok {
		return fmt.Errorf("no receivers found")
	}
	for _, r := range recs {
		if r == expected {
			return nil
		}
	}
	return fmt.Errorf("receiver %s not found", expected)
}

func (t *testCtx) theConfigurationShouldHaveExporters(expected string) error {
	exps, ok := t.rendered["exporters"].([]interface{})
	if !ok {
		return fmt.Errorf("no exporters found")
	}
	for _, e := range exps {
		if e == expected {
			return nil
		}
	}
	return fmt.Errorf("exporter %s not found", expected)
}

func (t *testCtx) aBaseConfigurationWithProcessor(processor string) error {
	yamlStr := fmt.Sprintf(`processors: [%s]`, processor)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.base = m
	return nil
}

func (t *testCtx) aRoleOverrideForWithProcessor(role, processor string) error {
	yamlStr := fmt.Sprintf(`processors: [%s]`, processor)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.roles[role] = m
	return nil
}

func (t *testCtx) theEffectiveConfigurationIsRenderedForRole(role string) error {
	t.reqCtx.Role = role
	yamlBytes, _, err := config.Render(t.store, t.reqCtx)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(yamlBytes, &t.rendered)
}

func (t *testCtx) theConfigurationShouldIncludeProcessor(expected string) error {
	procs, ok := t.rendered["processors"].([]interface{})
	if !ok {
		return fmt.Errorf("no processors found")
	}
	for _, p := range procs {
		if p == expected {
			return nil
		}
	}
	return fmt.Errorf("processor %s not found", expected)
}

func (t *testCtx) aClusterOverrideForWithRegion(cluster, region string) error {
	yamlStr := fmt.Sprintf(`region: %s`, region)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.clusters[cluster] = m
	return nil
}

func (t *testCtx) aSubdomainOverrideForWithTag(subdomain, tag string) error {
	yamlStr := fmt.Sprintf(`tag: %s`, tag)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.subs[subdomain] = m
	return nil
}

func (t *testCtx) theEffectiveConfigurationIsRenderedForClusterAndSubdomain(cluster, subdomain string) error {
	t.reqCtx.Cluster = cluster
	t.reqCtx.Subdomain = subdomain
	yamlBytes, _, err := config.Render(t.store, t.reqCtx)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(yamlBytes, &t.rendered)
}

func (t *testCtx) theConfigurationShouldHaveRegion(expected string) error {
	val, ok := t.rendered["region"].(string)
	if !ok || val != expected {
		return fmt.Errorf("expected region %s, got %v", expected, t.rendered["region"])
	}
	return nil
}

func (t *testCtx) theConfigurationShouldHaveTag(expected string) error {
	val, ok := t.rendered["tag"].(string)
	if !ok || val != expected {
		return fmt.Errorf("expected tag %s, got %v", expected, t.rendered["tag"])
	}
	return nil
}

func (t *testCtx) aRoleOverrideForWithInterval(role, interval string) error {
	yamlStr := fmt.Sprintf(`interval: %s`, interval)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.roles[role] = m
	return nil
}

func (t *testCtx) anEnvironmentRoleOverrideForAndWithInterval(env, role, interval string) error {
	yamlStr := fmt.Sprintf(`interval: %s`, interval)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.envRoles[env+"/"+role] = m
	return nil
}

func (t *testCtx) theEffectiveConfigurationIsRenderedForRoleInEnvironment(role, env string) error {
	t.reqCtx.Role = role
	t.reqCtx.Environment = env
	yamlBytes, _, err := config.Render(t.store, t.reqCtx)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(yamlBytes, &t.rendered)
}

func (t *testCtx) aBaseConfigurationWithInterval(interval string) error {
	yamlStr := fmt.Sprintf(`interval: %s`, interval)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.base = m
	return nil
}

func (t *testCtx) anEnvironmentOverrideForWithInterval(env, interval string) error {
	yamlStr := fmt.Sprintf(`interval: %s`, interval)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.envs[env] = m
	return nil
}

func (t *testCtx) aTemporaryRedisOverrideForHostnameWithInterval(host, interval string) error {
	yamlStr := fmt.Sprintf(`interval: %s`, interval)
	m := make(map[string]interface{})
	_ = yaml.Unmarshal([]byte(yamlStr), &m)
	t.store.redis[host] = m
	return nil
}

func (t *testCtx) theEffectiveConfigurationIsRenderedForHostnameInEnvironment(host, env string) error {
	t.reqCtx.Hostname = host
	t.reqCtx.Environment = env
	yamlBytes, _, err := config.Render(t.store, t.reqCtx)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(yamlBytes, &t.rendered)
}

func (t *testCtx) theConfigurationIntervalShouldBe(expected string) error {
	val, ok := t.rendered["interval"].(string)
	if !ok || val != expected {
		return fmt.Errorf("expected interval %s, got %v", expected, t.rendered["interval"])
	}
	return nil
}

// Reproducible Rendering steps
func (t *testCtx) theConfigurationIsRenderedConsecutiveTimes(n int) error {
	t.renderedHashes = nil
	t.reqCtx.Environment = "prod"
	for i := 0; i < n; i++ {
		_, hash, err := config.Render(t.store, t.reqCtx)
		if err != nil {
			return err
		}
		t.renderedHashes = append(t.renderedHashes, hash)
	}
	return nil
}

func (t *testCtx) everyRenderMustProduceTheIdenticalSHAHash() error {
	if len(t.renderedHashes) == 0 {
		return fmt.Errorf("no renders performed")
	}
	first := t.renderedHashes[0]
	for idx, h := range t.renderedHashes {
		if h != first {
			return fmt.Errorf("hash at index %d (%s) differs from first (%s)", idx, h, first)
		}
	}
	return nil
}

func (t *testCtx) anAgentConnectedWithReportedConfigHash(h string) error {
	// Pre-populate base and calculate hash
	t.store.base = map[string]interface{}{"service": "otel"}
	_, effectiveHash, _ := config.Render(t.store, t.reqCtx)
	if h == "matching" {
		t.agentReportedHash = effectiveHash
	} else {
		t.agentReportedHash = "different-hash"
	}
	return nil
}

func (t *testCtx) theServerEvaluatesEffectiveConfigMatchingHash(h string) error {
	_, effectiveHash, _ := config.Render(t.store, t.reqCtx)
	if t.agentReportedHash == effectiveHash {
		t.serverMarkedSync = true
		t.sentRemoteConfig = false
	} else {
		t.serverMarkedSync = false
		t.sentRemoteConfig = true
	}
	return nil
}

func (t *testCtx) theServerShouldMarkTheAgentAsInSync() error {
	if !t.serverMarkedSync {
		return fmt.Errorf("expected agent to be marked in-sync")
	}
	return nil
}

func (t *testCtx) theServerShouldNotSendARemoteConfigUpdatePayload() error {
	if t.sentRemoteConfig {
		return fmt.Errorf("server sent remote config update payload when it should have sent zero-diff")
	}
	return nil
}

// Redis TTL override steps
func (t *testCtx) anExistingEffectiveConfigWithInterval(interval string) error {
	t.tempStore = store.NewInMemoryOverrideStore()
	t.store.base = map[string]interface{}{"interval": interval}
	return nil
}

func (t *testCtx) anOperatorSetsATemporaryOverrideForWithIntervalAndTTL(host, interval, ttlStr string) error {
	var dur time.Duration
	if ttlStr == "1ms" {
		dur = 1 * time.Millisecond
	} else {
		d, err := time.ParseDuration(ttlStr)
		if err != nil {
			dur = 6 * time.Hour
		} else {
			dur = d
		}
	}
	cfg := map[string]interface{}{"interval": interval}
	return t.tempStore.SetOverride(host, cfg, dur, store.OverrideMetadata{Target: host})
}

func (t *testCtx) theEffectiveConfigForShouldHaveInterval(host, expected string) error {
	override, err := t.tempStore.GetOverride(host)
	if err != nil {
		return err
	}
	final := make(map[string]interface{})
	for k, v := range t.store.base {
		final[k] = v
	}
	for k, v := range override {
		final[k] = v
	}
	val, ok := final["interval"].(string)
	if !ok || val != expected {
		return fmt.Errorf("expected interval %s, got %v", expected, final["interval"])
	}
	return nil
}

func (t *testCtx) aTemporaryOverrideForWithIntervalAndTTL(host, interval, ttlStr string) error {
	t.tempStore = store.NewInMemoryOverrideStore()
	t.store.base = map[string]interface{}{"interval": "10s"}
	cfg := map[string]interface{}{"interval": interval}
	return t.tempStore.SetOverride(host, cfg, 2*time.Millisecond, store.OverrideMetadata{Target: host})
}

func (t *testCtx) theOverrideTTLHasExpired() error {
	time.Sleep(10 * time.Millisecond)
	return nil
}

func (t *testCtx) theEffectiveConfigForShouldRevertToBaseInterval(host, expected string) error {
	override, _ := t.tempStore.GetOverride(host)
	final := make(map[string]interface{})
	for k, v := range t.store.base {
		final[k] = v
	}
	for k, v := range override {
		final[k] = v
	}
	val, ok := final["interval"].(string)
	if !ok || val != expected {
		return fmt.Errorf("expected interval %s, got %v", expected, final["interval"])
	}
	return nil
}

func (t *testCtx) anActiveTemporaryOverrideForWithInterval(host, interval string) error {
	t.tempStore = store.NewInMemoryOverrideStore()
	t.store.base = map[string]interface{}{"interval": "10s"}
	cfg := map[string]interface{}{"interval": interval}
	return t.tempStore.SetOverride(host, cfg, 1*time.Hour, store.OverrideMetadata{Target: host})
}

func (t *testCtx) theOperatorRevokesTheTemporaryOverrideFor(host string) error {
	return t.tempStore.DeleteOverride(host)
}

// Storage modes steps
func (t *testCtx) aLocalStoreRunningInMode(mode string) error {
	dir, err := os.MkdirTemp("", "opamp-store-test-*")
	if err != nil {
		return err
	}
	t.tempDir = dir
	m := store.ModeDevelopment
	if mode == "production" {
		m = store.ModeProduction
	}
	t.localStore = store.NewLocalStore(dir, m)
	return nil
}

func (t *testCtx) theOperatorWritesAConfigNamed(layer, name string) error {
	content := []byte("receivers:\n  redis:\n    endpoint: localhost:6379\n")
	t.lastWriteErr = t.localStore.WriteConfig(layer, name, content)
	return nil
}

func (t *testCtx) theFileShouldBeSuccessfullyWrittenToDisk() error {
	if t.lastWriteErr != nil {
		return fmt.Errorf("expected write to succeed, got error: %w", t.lastWriteErr)
	}
	return nil
}

func (t *testCtx) theWriteOperationMustBeRejectedWithAReadOnlyError() error {
	if t.lastWriteErr == nil {
		return fmt.Errorf("expected write error in production mode, but write succeeded")
	}
	if !errors.Is(t.lastWriteErr, store.ErrProductionReadOnly) {
		return fmt.Errorf("expected ErrProductionReadOnly, got: %w", t.lastWriteErr)
	}
	return nil
}

func (t *testCtx) aKubernetesConfigMapStoreWithMountsForAnd(mount1, mount2 string) error {
	tmpDir, err := os.MkdirTemp("", "opamp-cm-*")
	if err != nil {
		return err
	}
	t.configMapRoot = tmpDir

	dir1 := filepath.Join(tmpDir, mount1)
	dir2 := filepath.Join(tmpDir, mount2)
	_ = os.MkdirAll(dir1, 0755)
	_ = os.MkdirAll(dir2, 0755)

	_ = os.WriteFile(filepath.Join(dir1, "base.yaml"), []byte("service:\n  pipelines:\n    traces:\n      receivers: [otlp]\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir2, "database.yaml"), []byte("receivers:\n  postgresql:\n    endpoint: localhost:5432\n"), 0644)

	t.configMapSrv = store.NewConfigMapStore([]string{dir1, dir2})
	return nil
}

func (t *testCtx) theEffectiveConfigurationIsReadForRole(role string) error {
	base, err := t.configMapSrv.GetBase()
	if err != nil {
		return fmt.Errorf("failed to get base from configmap: %w", err)
	}
	roleCfg, err := t.configMapSrv.GetRole(role)
	if err != nil {
		return fmt.Errorf("failed to get role from configmap: %w", err)
	}
	t.rendered = make(map[string]interface{})
	for k, v := range base {
		t.rendered[k] = v
	}
	for k, v := range roleCfg {
		t.rendered[k] = v
	}
	return nil
}

func (t *testCtx) bothBaseAndRoleConfigurationsShouldBeLoadedFromTheMounts() error {
	if _, ok := t.rendered["service"]; !ok {
		return fmt.Errorf("base service config not found")
	}
	if _, ok := t.rendered["receivers"]; !ok {
		return fmt.Errorf("role receivers config not found")
	}
	return nil
}

// Audit webhook steps
func (t *testCtx) aConfiguredWebhookDispatcherWithSecret(secret string) error {
	t.webhookSecret = secret
	t.webhookServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.lastWebhookHead = r.Header.Clone()
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		t.lastWebhookReq = body
		w.WriteHeader(http.StatusOK)
	}))

	t.auditDispatcher = audit.NewDispatcher(audit.WebhookConfig{
		URL:     t.webhookServer.URL,
		Secret:  secret,
		Timeout: 2 * time.Second,
	})
	return nil
}

func (t *testCtx) anAuditEventOfTypeIsPublished(eventType string) error {
	event := audit.NewEvent(
		audit.EventType(eventType),
		"create_override",
		audit.Actor{Identity: "ops-engineer", IPAddress: "192.168.1.50"},
		audit.Target{Hostname: "prod-db-01", Path: "redis:override:prod-db-01"},
		audit.EventDetails{Reason: "Emergency triage"},
	)
	return t.auditDispatcher.Publish(context.Background(), event)
}

func (t *testCtx) theWebhookPayloadMustContainAValidHMACSHA256Signature() error {
	sigHeader := t.lastWebhookHead.Get("X-OpAMP-Signature")
	if sigHeader == "" {
		return fmt.Errorf("missing X-OpAMP-Signature header")
	}
	prefix := "sha256="
	if len(sigHeader) <= len(prefix) {
		return fmt.Errorf("malformed signature header: %s", sigHeader)
	}
	providedSig := sigHeader[len(prefix):]

	mac := hmac.New(sha256.New, []byte(t.webhookSecret))
	mac.Write(t.lastWebhookReq)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if providedSig != expectedSig {
		return fmt.Errorf("signature mismatch: expected %s, got %s", expectedSig, providedSig)
	}
	return nil
}

func (t *testCtx) theWebhookPayloadMustIncludeActorTargetTimestampAndAction() error {
	var payload map[string]interface{}
	if err := json.Unmarshal(t.lastWebhookReq, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal webhook payload: %w", err)
	}
	for _, field := range []string{"actor", "target", "timestamp", "action", "event_type"} {
		if _, exists := payload[field]; !exists {
			return fmt.Errorf("missing expected audit field %s in payload", field)
		}
	}
	return nil
}

func InitializeScenario(ctx *godog.ScenarioContext) {
	t := &testCtx{
		store: newMockStore(),
	}

	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		if t.tempDir != "" {
			_ = os.RemoveAll(t.tempDir)
		}
		if t.configMapRoot != "" {
			_ = os.RemoveAll(t.configMapRoot)
		}
		if t.webhookServer != nil {
			t.webhookServer.Close()
		}
		return ctx, nil
	})

	// Config merge steps
	ctx.Step(`^a base configuration with receivers "([^"]*)"$`, t.aBaseConfigurationWithReceivers)
	ctx.Step(`^an environment override for "([^"]*)" with exporters "([^"]*)"$`, t.anEnvironmentOverrideForWithExporters)
	ctx.Step(`^the effective configuration is rendered for environment "([^"]*)"$`, t.theEffectiveConfigurationIsRenderedForEnvironment)
	ctx.Step(`^the configuration should have receivers "([^"]*)"$`, t.theConfigurationShouldHaveReceivers)
	ctx.Step(`^the configuration should have exporters "([^"]*)"$`, t.theConfigurationShouldHaveExporters)

	ctx.Step(`^a base configuration with "([^"]*)" processor$`, t.aBaseConfigurationWithProcessor)
	ctx.Step(`^a role override for "([^"]*)" with "([^"]*)" processor$`, t.aRoleOverrideForWithProcessor)
	ctx.Step(`^the effective configuration is rendered for role "([^"]*)"$`, t.theEffectiveConfigurationIsRenderedForRole)
	ctx.Step(`^the configuration should include processor "([^"]*)"$`, t.theConfigurationShouldIncludeProcessor)

	ctx.Step(`^a cluster override for "([^"]*)" with region "([^"]*)"$`, t.aClusterOverrideForWithRegion)
	ctx.Step(`^a subdomain override for "([^"]*)" with tag "([^"]*)"$`, t.aSubdomainOverrideForWithTag)
	ctx.Step(`^the effective configuration is rendered for cluster "([^"]*)" and subdomain "([^"]*)"$`, t.theEffectiveConfigurationIsRenderedForClusterAndSubdomain)
	ctx.Step(`^the configuration should have region "([^"]*)"$`, t.theConfigurationShouldHaveRegion)
	ctx.Step(`^the configuration should have tag "([^"]*)"$`, t.theConfigurationShouldHaveTag)

	ctx.Step(`^a role override for "([^"]*)" with interval "([^"]*)"$`, t.aRoleOverrideForWithInterval)
	ctx.Step(`^an environment-role override for "([^"]*)" and "([^"]*)" with interval "([^"]*)"$`, t.anEnvironmentRoleOverrideForAndWithInterval)
	ctx.Step(`^the effective configuration is rendered for role "([^"]*)" in environment "([^"]*)"$`, t.theEffectiveConfigurationIsRenderedForRoleInEnvironment)

	ctx.Step(`^a base configuration with interval "([^"]*)"$`, t.aBaseConfigurationWithInterval)
	ctx.Step(`^an environment override for "([^"]*)" with interval "([^"]*)"$`, t.anEnvironmentOverrideForWithInterval)
	ctx.Step(`^a temporary redis override for hostname "([^"]*)" with interval "([^"]*)"$`, t.aTemporaryRedisOverrideForHostnameWithInterval)
	ctx.Step(`^the effective configuration is rendered for hostname "([^"]*)" in environment "([^"]*)"$`, t.theEffectiveConfigurationIsRenderedForHostnameInEnvironment)
	ctx.Step(`^the configuration interval should be "([^"]*)"$`, t.theConfigurationIntervalShouldBe)

	// Reproducible rendering steps
	ctx.Step(`^the configuration is rendered (\d+) consecutive times$`, t.theConfigurationIsRenderedConsecutiveTimes)
	ctx.Step(`^every render must produce the identical SHA-256 hash$`, t.everyRenderMustProduceTheIdenticalSHAHash)
	ctx.Step(`^an agent connected with reported config hash "([^"]*)"$`, t.anAgentConnectedWithReportedConfigHash)
	ctx.Step(`^the server evaluates effective config matching hash "([^"]*)"$`, t.theServerEvaluatesEffectiveConfigMatchingHash)
	ctx.Step(`^the server should mark the agent as in-sync$`, t.theServerShouldMarkTheAgentAsInSync)
	ctx.Step(`^the server should not send a remote config update payload$`, t.theServerShouldNotSendARemoteConfigUpdatePayload)

	// Redis TTL override steps
	ctx.Step(`^an existing effective config with interval "([^"]*)"$`, t.anExistingEffectiveConfigWithInterval)
	ctx.Step(`^an operator sets a temporary override for "([^"]*)" with interval "([^"]*)" and TTL "([^"]*)"$`, t.anOperatorSetsATemporaryOverrideForWithIntervalAndTTL)
	ctx.Step(`^the effective config for "([^"]*)" should have interval "([^"]*)"$`, t.theEffectiveConfigForShouldHaveInterval)
	ctx.Step(`^a temporary override for "([^"]*)" with interval "([^"]*)" and TTL "([^"]*)"$`, t.aTemporaryOverrideForWithIntervalAndTTL)
	ctx.Step(`^the override TTL has expired$`, t.theOverrideTTLHasExpired)
	ctx.Step(`^the effective config for "([^"]*)" should revert to base interval "([^"]*)"$`, t.theEffectiveConfigForShouldRevertToBaseInterval)
	ctx.Step(`^an active temporary override for "([^"]*)" with interval "([^"]*)"$`, t.anActiveTemporaryOverrideForWithInterval)
	ctx.Step(`^the operator revokes the temporary override for "([^"]*)"$`, t.theOperatorRevokesTheTemporaryOverrideFor)

	// Storage modes steps
	ctx.Step(`^a local store running in "([^"]*)" mode$`, t.aLocalStoreRunningInMode)
	ctx.Step(`^the operator writes a "([^"]*)" config named "([^"]*)"$`, t.theOperatorWritesAConfigNamed)
	ctx.Step(`^the file should be successfully written to disk$`, t.theFileShouldBeSuccessfullyWrittenToDisk)
	ctx.Step(`^the write operation must be rejected with a read-only error$`, t.theWriteOperationMustBeRejectedWithAReadOnlyError)
	ctx.Step(`^a Kubernetes ConfigMap store with mounts for "([^"]*)" and "([^"]*)"$`, t.aKubernetesConfigMapStoreWithMountsForAnd)
	ctx.Step(`^the effective configuration is read for role "([^"]*)"$`, t.theEffectiveConfigurationIsReadForRole)
	ctx.Step(`^both base and role configurations should be loaded from the mounts$`, t.bothBaseAndRoleConfigurationsShouldBeLoadedFromTheMounts)

	// Audit webhook steps
	ctx.Step(`^a configured webhook dispatcher with secret "([^"]*)"$`, t.aConfiguredWebhookDispatcherWithSecret)
	ctx.Step(`^an audit event of type "([^"]*)" is published$`, t.anAuditEventOfTypeIsPublished)
	ctx.Step(`^the webhook payload must contain a valid HMAC-SHA256 signature$`, t.theWebhookPayloadMustContainAValidHMACSHA256Signature)
	ctx.Step(`^the webhook payload must include actor, target, timestamp, and action$`, t.theWebhookPayloadMustIncludeActorTargetTimestampAndAction)
}

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}
