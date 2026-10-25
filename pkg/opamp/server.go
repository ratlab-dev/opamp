package opamp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/naugustin/opamp-server/pkg/audit"
	"github.com/naugustin/opamp-server/pkg/config"
	"github.com/naugustin/opamp-server/pkg/store"
	clientTypes "github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	serverTypes "github.com/open-telemetry/opamp-go/server/types"
	"github.com/redis/go-redis/v9"
)

type stdLogger struct{}

func (l *stdLogger) Debugf(ctx context.Context, format string, v ...interface{}) {
	// log.Printf("[DEBUG] "+format, v...)
}

func (l *stdLogger) Errorf(ctx context.Context, format string, v ...interface{}) {
	log.Printf("[ERROR] "+format, v...)
}

// Server wraps the OpAMP server library, agent registry, and config merger
type Server struct {
	opampServer  server.OpAMPServer
	appStore     store.AppStore
	auditPub     *audit.Dispatcher
	registry     *AgentRegistry
	broker       *EventBroker
	redisStore   *store.RedisOverrideStore
	nodeID       string
	handlerFunc  server.HTTPHandlerFunc
	connContext  server.ConnContext

	mu          sync.Mutex
	activeConns map[string]serverTypes.Connection // instanceUID hex -> Connection
}

func NewServer(appStore store.AppStore, auditPub *audit.Dispatcher, redisStore *store.RedisOverrideStore, nodeID string) (*Server, error) {
	if nodeID == "" {
		nodeID = "server-node-1"
	}

	var rdb *redis.Client
	if redisStore != nil {
		rdb = redisStore.Client()
	}

	broker := NewEventBroker()
	registry := NewAgentRegistry(rdb, nodeID)

	s := &Server{
		appStore:    appStore,
		auditPub:    auditPub,
		registry:    registry,
		broker:      broker,
		redisStore:  redisStore,
		nodeID:      nodeID,
		activeConns: make(map[string]serverTypes.Connection),
	}

	// In HA mode, subscribe to Redis cluster broadcasts from peer server nodes
	if redisStore != nil && rdb != nil {
		redisStore.SubscribeClusterEvents(context.Background(), "opamp:cluster:broadcast", func(payload string) {
			s.pushLocalDiffs(context.Background())
			s.broker.Broadcast("cluster_sync", map[string]string{"event": payload})
		})
	}

	opSrv := server.New(&stdLogger{})
	handlerFunc, connCtx, err := opSrv.Attach(server.Settings{
		Callbacks: serverTypes.Callbacks{
			OnConnecting: s.onConnecting,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to attach opamp server: %w", err)
	}

	s.opampServer = opSrv
	s.handlerFunc = handlerFunc
	s.connContext = connCtx

	return s, nil
}

func (s *Server) Registry() *AgentRegistry {
	return s.registry
}

func (s *Server) Broker() *EventBroker {
	return s.broker
}

func (s *Server) HTTPHandler() http.HandlerFunc {
	return http.HandlerFunc(s.handlerFunc)
}

func (s *Server) ConnContext() server.ConnContext {
	return s.connContext
}

func (s *Server) onConnecting(request *http.Request) serverTypes.ConnectionResponse {
	return serverTypes.ConnectionResponse{
		Accept: true,
		ConnectionCallbacks: serverTypes.ConnectionCallbacks{
			OnConnected: func(ctx context.Context, conn serverTypes.Connection) {
				// Handled on first message
			},
			OnMessage: func(ctx context.Context, conn serverTypes.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
				return s.handleMessage(ctx, conn, message)
			},
			OnConnectionClose: func(conn serverTypes.Connection) {
				s.onConnectionClose(conn)
			},
		},
	}
}

func (s *Server) onConnectionClose(conn serverTypes.Connection) {
	s.mu.Lock()
	var closedUID string
	for uid, c := range s.activeConns {
		if c == conn {
			closedUID = uid
			delete(s.activeConns, uid)
			break
		}
	}
	s.mu.Unlock()

	if closedUID != "" {
		s.broker.Broadcast("agent_disconnected", map[string]string{"instance_uid": closedUID})
	}
}

func (s *Server) handleMessage(ctx context.Context, conn serverTypes.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	uid := msg.InstanceUid
	uidHex := hex.EncodeToString(uid)

	var remoteAddr string
	if conn.Connection() != nil && conn.Connection().RemoteAddr() != nil {
		remoteAddr = conn.Connection().RemoteAddr().String()
	}

	s.mu.Lock()
	s.activeConns[uidHex] = conn
	s.mu.Unlock()

	// Update registry with agent description, remote status, and reported effective config
	agentInfo := s.registry.RegisterOrUpdate(uid, remoteAddr, msg.AgentDescription, msg.RemoteConfigStatus, msg.EffectiveConfig)

	// Determine render context from agent attributes
	reqCtx := s.registry.ToRenderContext(agentInfo)

	// Render effective YAML & deterministic SHA256 hash
	yamlBytes, hashStr, err := config.Render(s.appStore, reqCtx)
	if err != nil {
		log.Printf("[ERROR] failed to render config for agent %s (%s): %v", uidHex, agentInfo.Hostname, err)
		return &protobufs.ServerToAgent{
			InstanceUid: uid,
			ErrorResponse: &protobufs.ServerErrorResponse{
				Type:         protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest,
				ErrorMessage: fmt.Sprintf("failed to render config: %v", err),
			},
		}
	}

	s.registry.UpdateEffectiveConfig(uidHex, hashStr, string(yamlBytes))

	hashBytes, _ := hex.DecodeString(hashStr)
	if len(hashBytes) == 0 {
		hashBytes = sha256Bytes(yamlBytes)
	}

	// Server announces capabilities: Offers remote config, accepts status, AND accepts effective config!
	serverCapabilities := uint64(
		protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig |
			protobufs.ServerCapabilities_ServerCapabilities_AcceptsEffectiveConfig |
			protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus,
	)

	resp := &protobufs.ServerToAgent{
		InstanceUid:  uid,
		Capabilities: serverCapabilities,
	}

	// Check if agent already has this exact hash applied (Zero-Diff Check)
	hasMatchingHash := false
	if msg.RemoteConfigStatus != nil && len(msg.RemoteConfigStatus.LastRemoteConfigHash) > 0 {
		if bytes.Equal(msg.RemoteConfigStatus.LastRemoteConfigHash, hashBytes) {
			hasMatchingHash = true
		}
	} else if agentInfo.LastRemoteHash == hashStr && agentInfo.Status == "IN_SYNC" {
		hasMatchingHash = true
	} else if agentInfo.ReportedConfigYAML != "" && agentInfo.ReportedConfigYAML == string(yamlBytes) {
		hasMatchingHash = true
	}

	if hasMatchingHash {
		// Agent is already up to date!
		// DO NOT send remote_config to prevent unnecessary collector restart
		agentInfo.Status = "IN_SYNC"
		agentInfo.LastRemoteHash = hashStr
	} else {
		// Deliver remote config to agent
		resp.RemoteConfig = &protobufs.AgentRemoteConfig{
			ConfigHash: hashBytes,
			Config: &protobufs.AgentConfigMap{
				ConfigMap: map[string]*protobufs.AgentConfigObject{
					"": {
						Body:        yamlBytes,
						ContentType: "text/yaml",
					},
				},
			},
		}
		agentInfo.Status = "APPLYING"

		// Publish audit event for config delivery
		if s.auditPub != nil {
			s.auditPub.PublishAsync(audit.NewEvent(
				audit.EventAgentConfigSynced,
				"deliver_remote_config",
				audit.Actor{Identity: "opamp-server (" + s.nodeID + ")"},
				audit.Target{
					Hostname:    agentInfo.Hostname,
					Environment: agentInfo.Environment,
					Role:        agentInfo.Role,
					Cluster:     agentInfo.Cluster,
					Subdomain:   agentInfo.Subdomain,
				},
				audit.EventDetails{
					PreviousHash: agentInfo.LastRemoteHash,
					NewHash:      hashStr,
					Reason:       "Delivering updated remote config",
				},
			))
		}
	}

	// Notify real-time UI dashboard of agent activity
	s.broker.Broadcast("agent_update", agentInfo)

	return resp
}

// BroadcastConfigCheck triggers cluster-wide and local push of updated configs
func (s *Server) BroadcastConfigCheck(ctx context.Context) {
	// 1. If in HA mode, publish cluster broadcast so other server nodes also push to their agents
	if s.redisStore != nil {
		_ = s.redisStore.PublishClusterEvent(ctx, "opamp:cluster:broadcast", "config_changed")
	}

	// 2. Push to local connections
	s.pushLocalDiffs(ctx)

	// 3. Notify real-time UI
	s.broker.Broadcast("config_update", map[string]string{"status": "applied"})
}

func (s *Server) pushLocalDiffs(ctx context.Context) {
	s.mu.Lock()
	connsCopy := make(map[string]serverTypes.Connection, len(s.activeConns))
	for k, v := range s.activeConns {
		connsCopy[k] = v
	}
	s.mu.Unlock()

	for uidHex, conn := range connsCopy {
		agentInfo, ok := s.registry.GetAgent(uidHex)
		if !ok {
			continue
		}

		reqCtx := s.registry.ToRenderContext(agentInfo)
		yamlBytes, hashStr, err := config.Render(s.appStore, reqCtx)
		if err != nil {
			continue
		}

		// Only push if hash differs from agent's last applied hash
		if agentInfo.LastRemoteHash == hashStr {
			continue // No diff, skip push to prevent restart
		}

		hashBytes, _ := hex.DecodeString(hashStr)
		uidBytes, _ := hex.DecodeString(uidHex)

		serverCapabilities := uint64(
			protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig |
				protobufs.ServerCapabilities_ServerCapabilities_AcceptsEffectiveConfig |
				protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus,
		)

		msg := &protobufs.ServerToAgent{
			InstanceUid:  uidBytes,
			Capabilities: serverCapabilities,
			RemoteConfig: &protobufs.AgentRemoteConfig{
				ConfigHash: hashBytes,
				Config: &protobufs.AgentConfigMap{
					ConfigMap: map[string]*protobufs.AgentConfigObject{
						"": {
							Body:        yamlBytes,
							ContentType: "text/yaml",
						},
					},
				},
			},
		}

		s.registry.UpdateEffectiveConfig(uidHex, hashStr, string(yamlBytes))
		agentInfo.Status = "APPLYING"

		_ = conn.Send(ctx, msg)
		s.broker.Broadcast("agent_update", agentInfo)
	}
}

func sha256Bytes(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

var _ clientTypes.Logger = (*stdLogger)(nil)
