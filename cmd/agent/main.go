package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

type customLogger struct{}

func (l customLogger) Debugf(ctx context.Context, format string, v ...interface{}) {
	// Keep verbose protocol debug muted by default
}

func (l customLogger) Errorf(ctx context.Context, format string, v ...interface{}) {
	log.Printf("[OpAMP-Client-ERROR] "+format, v...)
}

type SupervisorAgent struct {
	serverURL  string
	role       string
	env        string
	cluster    string
	subdomain  string
	hostname   string
	otelBin    string
	configPath string

	opampClient client.OpAMPClient
	instanceUID types.InstanceUid

	mu             sync.Mutex
	effectiveYAML  string
	lastRemoteHash []byte

	collectorCmd *exec.Cmd
}

func main() {
	serverURL := flag.String("server-url", "ws://127.0.0.1:4320/v1/opamp", "OpAMP server WebSocket URL")
	role := flag.String("role", "mac-work", "Agent topology role (e.g. mac-work, mac-dev, database, node-exporter)")
	env := flag.String("env", "dev", "Deployment environment (e.g. dev, prod, staging)")
	cluster := flag.String("cluster", "local", "Cluster name (e.g. local, us-east-1)")
	subdomain := flag.String("subdomain", "", "Subdomain scope (e.g. analytics, payments)")
	hostname := flag.String("hostname", "", "Host identifier (defaults to system hostname)")
	otelBin := flag.String("otel-bin", "", "Path to OpenTelemetry Collector binary (if available)")
	configPath := flag.String("config-path", "", "Path to write received configuration (defaults to ~/.opamp-agent/config.yaml)")

	flag.Parse()

	// Default hostname
	h := *hostname
	if h == "" {
		if osH, err := os.Hostname(); err == nil && osH != "" {
			h = osH
		} else {
			h = "mac-host"
		}
	}

	// Default config path
	cfgPath := *configPath
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "."
		}
		cfgPath = filepath.Join(home, ".opamp-agent", "config.yaml")
	}

	// Try to find otelcol on PATH or local bin/collector if not specified
	binPath := *otelBin
	if binPath == "" {
		candidates := []string{
			"bin/collector/otelcol-contrib",
			"./bin/collector/otelcol-contrib",
			"bin/collector/otelcol",
			"./bin/collector/otelcol",
			"otelcol-contrib",
			"otelcol",
			"otel-collector",
		}
		for _, name := range candidates {
			if strings.Contains(name, "/") {
				if fi, err := os.Stat(name); err == nil && !fi.IsDir() {
					if abs, absErr := filepath.Abs(name); absErr == nil {
						binPath = abs
						break
					}
					binPath = name
					break
				}
			} else if p, err := exec.LookPath(name); err == nil {
				binPath = p
				break
			}
		}
	}

	uid := getOrCreateInstanceUID()

	agent := &SupervisorAgent{
		serverURL:   *serverURL,
		role:        *role,
		env:         *env,
		cluster:     *cluster,
		subdomain:   *subdomain,
		hostname:    h,
		otelBin:     binPath,
		configPath:  cfgPath,
		instanceUID: uid,
	}

	if err := agent.Start(); err != nil {
		log.Fatalf("Failed to start OpAMP Agent: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down OpAMP Agent...")
	agent.Stop()
	log.Println("OpAMP Agent stopped.")
}

func getOrCreateInstanceUID() types.InstanceUid {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	dir := filepath.Join(home, ".opamp-agent")
	_ = os.MkdirAll(dir, 0755)

	uidFile := filepath.Join(dir, "instance_id")
	if data, err := os.ReadFile(uidFile); err == nil && len(data) == 16 {
		var uid types.InstanceUid
		copy(uid[:], data)
		return uid
	}

	// Generate persistent UUID
	newUUID := uuid.New()
	var uid types.InstanceUid
	copy(uid[:], newUUID[:])
	_ = os.WriteFile(uidFile, uid[:], 0600)
	return uid
}

func (a *SupervisorAgent) Start() error {
	log.Printf("==========================================================")
	log.Printf("Starting OpAMP Supervisor Agent (Role: %s | Host: %s)", a.role, a.hostname)
	log.Printf("Connecting to Server: %s", a.serverURL)
	log.Printf("Target Config Path:  %s", a.configPath)
	if a.otelBin != "" {
		log.Printf("OpenTelemetry Binary: %s", a.otelBin)
	} else {
		log.Printf("OpenTelemetry Binary: Not found on PATH (Simulating collector execution & live stdout output)")
	}
	log.Printf("==========================================================")

	a.opampClient = client.NewWebSocket(customLogger{})

	// Construct Agent Description with Topology Attributes
	descr := &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "io.opentelemetry.collector"}},
			},
			{
				Key:   "host.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: a.hostname}},
			},
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key:   "service.role",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: a.role}},
			},
			{
				Key:   "deployment.environment",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: a.env}},
			},
			{
				Key:   "k8s.cluster.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: a.cluster}},
			},
		},
	}
	if a.subdomain != "" {
		descr.NonIdentifyingAttributes = append(descr.NonIdentifyingAttributes, &protobufs.KeyValue{
			Key:   "service.subdomain",
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: a.subdomain}},
		})
	}

	if err := a.opampClient.SetAgentDescription(descr); err != nil {
		return fmt.Errorf("failed to set agent description: %w", err)
	}

	// If config file already exists on disk, seed initial RemoteConfigStatus & effectiveYAML
	var initialStatus *protobufs.RemoteConfigStatus
	if existingData, err := os.ReadFile(a.configPath); err == nil && len(existingData) > 0 {
		hash := sha256.Sum256(existingData)
		a.mu.Lock()
		a.effectiveYAML = string(existingData)
		a.lastRemoteHash = hash[:]
		a.mu.Unlock()
		initialStatus = &protobufs.RemoteConfigStatus{
			LastRemoteConfigHash: hash[:],
			Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		}
	}

	// Prepare StartSettings
	settings := types.StartSettings{
		OpAMPServerURL:     a.serverURL,
		InstanceUid:        a.instanceUID,
		RemoteConfigStatus: initialStatus,
		Capabilities: protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
			protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig,
		Callbacks: types.Callbacks{
			OnConnect: func(ctx context.Context) {
				log.Printf("[OpAMP] Connected to Server at %s", a.serverURL)
			},
			OnConnectFailed: func(ctx context.Context, err error) {
				log.Printf("[OpAMP] Connection attempt failed: %v (retrying...)", err)
			},
			OnError: func(ctx context.Context, err *protobufs.ServerErrorResponse) {
				log.Printf("[OpAMP] Server returned error: %s", err.ErrorMessage)
			},
			OnMessage: func(ctx context.Context, msg *types.MessageData) {
				a.handleMessage(ctx, msg)
			},
			GetEffectiveConfig: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
				return a.getEffectiveConfig(), nil
			},
		},
	}

	return a.opampClient.Start(context.Background(), settings)
}

func (a *SupervisorAgent) handleMessage(ctx context.Context, msg *types.MessageData) {
	if msg == nil || msg.RemoteConfig == nil {
		return
	}

	remoteCfg := msg.RemoteConfig
	configHash := remoteCfg.ConfigHash
	configHashHex := hex.EncodeToString(configHash)

	a.mu.Lock()
	if len(a.lastRemoteHash) > 0 && hex.EncodeToString(a.lastRemoteHash) == configHashHex {
		a.mu.Unlock()
		log.Printf("[OpAMP] Zero-Diff: Remote config hash %s matches current. Zero restarts needed.", configHashHex[:8])
		_ = a.opampClient.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
			LastRemoteConfigHash: configHash,
			Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		})
		_ = a.opampClient.UpdateEffectiveConfig(ctx)
		return
	}
	a.mu.Unlock()

	log.Printf("[OpAMP] Received remote configuration update (Hash: %s)", configHashHex)

	var configContent string
	if remoteCfg.Config != nil && len(remoteCfg.Config.ConfigMap) > 0 {
		for _, fileObj := range remoteCfg.Config.ConfigMap {
			if fileObj != nil && len(fileObj.Body) > 0 {
				configContent = string(fileObj.Body)
				break
			}
		}
	}

	if configContent == "" {
		log.Printf("[OpAMP] Empty configuration body received.")
		return
	}

	// 1. Write config to destination file
	_ = os.MkdirAll(filepath.Dir(a.configPath), 0755)
	if err := os.WriteFile(a.configPath, []byte(configContent), 0644); err != nil {
		log.Printf("[OpAMP-ERROR] Failed to write config file to %s: %v", a.configPath, err)
		_ = a.opampClient.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
			LastRemoteConfigHash: configHash,
			Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
			ErrorMessage:         err.Error(),
		})
		return
	}

	// 2. Apply config to Collector (Restart or start process)
	if err := a.applyConfiguration(configContent); err != nil {
		log.Printf("[OpAMP-ERROR] Failed to apply configuration to collector: %v", err)
		_ = a.opampClient.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
			LastRemoteConfigHash: configHash,
			Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
			ErrorMessage:         err.Error(),
		})
		return
	}

	a.mu.Lock()
	a.effectiveYAML = configContent
	a.lastRemoteHash = configHash
	a.mu.Unlock()

	// 3. Confirm APPLIED status back to OpAMP server
	_ = a.opampClient.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
		LastRemoteConfigHash: configHash,
		Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
	})

	// 4. Send updated EffectiveConfig back to OpAMP server for real-time zero-drift verification in UI
	_ = a.opampClient.UpdateEffectiveConfig(ctx)

	log.Printf("[OpAMP] Remote configuration applied successfully. Status reported: APPLIED (Zero Drift).")
}

func (a *SupervisorAgent) applyConfiguration(yamlContent string) error {
	log.Printf("----------------------------------------------------------")
	log.Printf("APPLYING CONFIGURATION TO COLLECTOR:")
	lines := strings.Split(yamlContent, "\n")
	previewLines := lines
	if len(previewLines) > 15 {
		previewLines = previewLines[:15]
	}
	for _, l := range previewLines {
		log.Printf("  | %s", l)
	}
	if len(lines) > 15 {
		log.Printf("  | ... (%d lines total written to %s)", len(lines), a.configPath)
	}
	log.Printf("----------------------------------------------------------")

	// If OpenTelemetry Collector binary is installed, manage the live process
	if a.otelBin != "" {
		a.mu.Lock()
		if a.collectorCmd != nil && a.collectorCmd.Process != nil {
			log.Printf("[Collector] Restarting collector process (PID %d)...", a.collectorCmd.Process.Pid)
			_ = a.collectorCmd.Process.Kill()
			_ = a.collectorCmd.Wait()
		}

		cmd := exec.Command(a.otelBin, "--config", a.configPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			a.mu.Unlock()
			return fmt.Errorf("failed to start collector binary: %w", err)
		}
		a.collectorCmd = cmd
		a.mu.Unlock()
		log.Printf("[Collector] Started OpenTelemetry collector successfully (PID %d)", cmd.Process.Pid)
	} else {
		log.Printf("[Collector] Live configuration active at: %s", a.configPath)
	}

	return nil
}

func (a *SupervisorAgent) getEffectiveConfig() *protobufs.EffectiveConfig {
	a.mu.Lock()
	defer a.mu.Unlock()

	body := a.effectiveYAML
	if body == "" {
		// Read from disk if exists
		if data, err := os.ReadFile(a.configPath); err == nil {
			body = string(data)
		}
	}

	if body == "" {
		body = "# No configuration applied yet\n"
	}

	return &protobufs.EffectiveConfig{
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigObject{
				"default": {
					Body: []byte(body),
				},
			},
		},
	}
}

func (a *SupervisorAgent) Stop() {
	a.mu.Lock()
	if a.collectorCmd != nil && a.collectorCmd.Process != nil {
		log.Printf("[Collector] Stopping collector process (PID %d)...", a.collectorCmd.Process.Pid)
		_ = a.collectorCmd.Process.Kill()
		_ = a.collectorCmd.Wait()
	}
	a.mu.Unlock()

	if a.opampClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = a.opampClient.Stop(ctx)
	}
}

// computeHash computes standard SHA256 hex
func computeHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
