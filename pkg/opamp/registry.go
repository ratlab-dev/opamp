package opamp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/naugustin/opamp-server/pkg/config"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/redis/go-redis/v9"
)

// AgentInfo represents a registered agent instance
type AgentInfo struct {
	InstanceUID         string            `json:"instance_uid"`
	Hostname            string            `json:"hostname"`
	Environment         string            `json:"environment"`
	Cluster             string            `json:"cluster"`
	Subdomain           string            `json:"subdomain"`
	Role                string            `json:"role"`
	Status              string            `json:"status"` // "IN_SYNC", "APPLYING", "FAILED", "CONNECTED"
	LastRemoteHash      string            `json:"last_remote_hash"`
	EffectiveHash       string            `json:"effective_hash"`
	ConnectedAt         time.Time         `json:"connected_at"`
	LastSeenAt          time.Time         `json:"last_seen_at"`
	RemoteAddr          string            `json:"remote_addr"`
	ServerNodeID        string            `json:"server_node_id,omitempty"`
	Attributes          map[string]string `json:"attributes"`
	EffectiveConfigYAML string            `json:"effective_config_yaml,omitempty"`
	ReportedConfigYAML  string            `json:"reported_config_yaml,omitempty"` // Actual live config running on VM
}

// AgentRegistry maintains connected and cluster-wide seen agents
type AgentRegistry struct {
	mu           sync.RWMutex
	agents       map[string]*AgentInfo
	redisClient  *redis.Client
	serverNodeID string
	stopChan     chan struct{}
}

func NewAgentRegistry(rdb *redis.Client, nodeID string) *AgentRegistry {
	if nodeID == "" {
		nodeID = "node-default"
	}
	r := &AgentRegistry{
		agents:       make(map[string]*AgentInfo),
		redisClient:  rdb,
		serverNodeID: nodeID,
		stopChan:     make(chan struct{}),
	}

	if rdb != nil {
		go r.leaseHeartbeatLoop()
	}

	return r
}

func (r *AgentRegistry) Close() {
	if r.redisClient != nil {
		select {
		case <-r.stopChan:
		default:
			close(r.stopChan)
		}
	}
}

func (r *AgentRegistry) leaseHeartbeatLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			return
		case <-ticker.C:
			r.mu.RLock()
			localUIDs := make([]string, 0, len(r.agents))
			for uid := range r.agents {
				localUIDs = append(localUIDs, uid)
			}
			r.mu.RUnlock()

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			pipe := r.redisClient.Pipeline()
			for _, uid := range localUIDs {
				leaseKey := "opamp:agent:lease:" + uid
				pipe.Set(ctx, leaseKey, r.serverNodeID, 35*time.Second)
			}
			_, _ = pipe.Exec(ctx)
			cancel()
		}
	}
}

func (r *AgentRegistry) RegisterOrUpdate(uid []byte, remoteAddr string, desc *protobufs.AgentDescription, status *protobufs.RemoteConfigStatus, effectiveCfg *protobufs.EffectiveConfig) *AgentInfo {
	r.mu.Lock()
	defer r.mu.Unlock()

	uidHex := hex.EncodeToString(uid)
	now := time.Now()

	info, exists := r.agents[uidHex]
	if !exists {
		info = &AgentInfo{
			InstanceUID:  uidHex,
			ConnectedAt:  now,
			ServerNodeID: r.serverNodeID,
			Attributes:   make(map[string]string),
		}
		r.agents[uidHex] = info
	}

	info.LastSeenAt = now
	info.RemoteAddr = remoteAddr
	info.ServerNodeID = r.serverNodeID

	// Extract reported effective config if sent by agent
	if effectiveCfg != nil && effectiveCfg.ConfigMap != nil {
		for _, obj := range effectiveCfg.ConfigMap.ConfigMap {
			if obj != nil && len(obj.Body) > 0 {
				info.ReportedConfigYAML = string(obj.Body)
				break
			}
		}
	}

	// Parse attributes if provided
	if desc != nil {
		for _, kv := range desc.IdentifyingAttributes {
			if kv != nil && kv.Value != nil {
				val := kv.Value.GetStringValue()
				info.Attributes[kv.Key] = val
				extractAttribute(info, kv.Key, val)
			}
		}
		for _, kv := range desc.NonIdentifyingAttributes {
			if kv != nil && kv.Value != nil {
				val := kv.Value.GetStringValue()
				info.Attributes[kv.Key] = val
				extractAttribute(info, kv.Key, val)
			}
		}
	}

	if status != nil {
		if len(status.LastRemoteConfigHash) > 0 {
			info.LastRemoteHash = hex.EncodeToString(status.LastRemoteConfigHash)
		}
		switch status.Status {
		case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED:
			info.Status = "IN_SYNC"
		case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING:
			info.Status = "APPLYING"
		case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED:
			info.Status = "FAILED"
		default:
			if info.Status == "" {
				info.Status = "CONNECTED"
			}
		}
	} else if info.Status == "" {
		info.Status = "CONNECTED"
	}

	// Persist to Redis in HA mode
	if r.redisClient != nil {
		r.persistAgentToRedis(info)
	}

	return info
}

func (r *AgentRegistry) persistAgentToRedis(info *AgentInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := json.Marshal(info)
	if err != nil {
		return
	}

	agentKey := "opamp:agent:" + info.InstanceUID
	leaseKey := "opamp:agent:lease:" + info.InstanceUID
	activeSet := "opamp:active_agents"

	pipe := r.redisClient.Pipeline()
	pipe.Set(ctx, agentKey, data, 24*time.Hour)
	pipe.Set(ctx, leaseKey, r.serverNodeID, 35*time.Second)
	pipe.SAdd(ctx, activeSet, info.InstanceUID)
	_, _ = pipe.Exec(ctx)
}

func (r *AgentRegistry) UpdateEffectiveConfig(uid string, hashStr, yamlStr string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agents[uid]
	if exists {
		info.EffectiveHash = hashStr
		info.EffectiveConfigYAML = yamlStr
		if r.redisClient != nil {
			r.persistAgentToRedis(info)
		}
	}
}

func (r *AgentRegistry) RemoveAgent(uidHex string) {
	r.mu.Lock()
	delete(r.agents, uidHex)
	r.mu.Unlock()

	if r.redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		leaseKey := "opamp:agent:lease:" + uidHex
		activeSet := "opamp:active_agents"

		pipe := r.redisClient.Pipeline()
		pipe.Del(ctx, leaseKey)
		pipe.SRem(ctx, activeSet, uidHex)
		_, _ = pipe.Exec(ctx)
	}
}

func extractAttribute(info *AgentInfo, k, v string) {
	switch k {
	case "host.name", "hostname", "node.name":
		if info.Hostname == "" {
			info.Hostname = v
		}
	case "deployment.environment", "environment", "env":
		if info.Environment == "" {
			info.Environment = v
		}
	case "k8s.cluster.name", "cluster", "cluster.name":
		if info.Cluster == "" {
			info.Cluster = v
		}
	case "service.subdomain", "subdomain":
		if info.Subdomain == "" {
			info.Subdomain = v
		}
	case "service.role", "role", "workload.role":
		if info.Role == "" {
			info.Role = v
		}
	}
}

func (r *AgentRegistry) ToRenderContext(info *AgentInfo) config.RenderContext {
	return config.RenderContext{
		Hostname:         info.Hostname,
		Environment:      info.Environment,
		Cluster:          info.Cluster,
		Subdomain:        info.Subdomain,
		Role:             info.Role,
		CustomAttributes: info.Attributes,
	}
}

func (r *AgentRegistry) ListAgents() []*AgentInfo {
	// If Redis is configured, fetch cluster-wide agents
	if r.redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		activeSet := "opamp:active_agents"
		uids, err := r.redisClient.SMembers(ctx, activeSet).Result()
		if err == nil && len(uids) > 0 {
			var list []*AgentInfo
			for _, uid := range uids {
				leaseKey := "opamp:agent:lease:" + uid
				exists, _ := r.redisClient.Exists(ctx, leaseKey).Result()
				if exists == 0 {
					// Lease expired, clean up
					r.redisClient.SRem(ctx, activeSet, uid)
					continue
				}

				agentKey := "opamp:agent:" + uid
				data, err := r.redisClient.Get(ctx, agentKey).Bytes()
				if err == nil {
					var info AgentInfo
					if err := json.Unmarshal(data, &info); err == nil {
						list = append(list, &info)
					}
				}
			}
			return list
		}
	}

	// Standalone in-memory fallback
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]*AgentInfo, 0, len(r.agents))
	for _, a := range r.agents {
		cp := *a
		list = append(list, &cp)
	}
	return list
}

func (r *AgentRegistry) GetAgent(uid string) (*AgentInfo, bool) {
	if r.redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		agentKey := "opamp:agent:" + uid
		data, err := r.redisClient.Get(ctx, agentKey).Bytes()
		if err == nil {
			var info AgentInfo
			if err := json.Unmarshal(data, &info); err == nil {
				return &info, true
			}
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	info, exists := r.agents[uid]
	if !exists {
		return nil, false
	}
	cp := *info
	return &cp, true
}
