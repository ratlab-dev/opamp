package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/naugustin/opamp-server/pkg/audit"
	"github.com/naugustin/opamp-server/pkg/opamp"
	"github.com/naugustin/opamp-server/pkg/store"
	"github.com/naugustin/opamp-server/pkg/ui"
)

func main() {
	var (
		port          = flag.String("port", getEnv("OPAMP_PORT", "4320"), "HTTP/WebSocket server port")
		modeStr       = flag.String("mode", getEnv("OPAMP_MODE", "development"), "Operating mode: development or production")
		configDir     = flag.String("config-dir", getEnv("OPAMP_CONFIG_DIR", "./examples/configs"), "Path to local config directory")
		configMaps    = flag.String("configmaps", getEnv("OPAMP_CONFIGMAPS", ""), "Comma-separated Kubernetes ConfigMap mount directory paths")
		giteaURL      = flag.String("gitea-url", getEnv("GITEA_URL", ""), "Gitea server base URL (e.g. https://gitea.example.com)")
		giteaOwner    = flag.String("gitea-owner", getEnv("GITEA_OWNER", ""), "Gitea repository owner")
		giteaRepo     = flag.String("gitea-repo", getEnv("GITEA_REPO", ""), "Gitea repository name")
		giteaBranch   = flag.String("gitea-branch", getEnv("GITEA_BRANCH", "main"), "Gitea default branch")
		giteaToken    = flag.String("gitea-token", getEnv("GITEA_TOKEN", ""), "Gitea API token")
		redisAddr     = flag.String("redis-addr", getEnv("REDIS_ADDR", ""), "Redis server address (e.g. localhost:6379, empty for in-memory)")
		redisPassword = flag.String("redis-password", getEnv("REDIS_PASSWORD", ""), "Redis password")
		webhookURL    = flag.String("webhook-url", getEnv("WEBHOOK_URL", ""), "Webhook URL for audit notifications")
		webhookSecret = flag.String("webhook-secret", getEnv("WEBHOOK_SECRET", ""), "HMAC secret for webhook audit signing")
		nodeID        = flag.String("node-id", getEnv("OPAMP_NODE_ID", "node-1"), "Server node ID for HA clustering")
	)
	flag.Parse()

	mode := store.ModeDevelopment
	if strings.ToLower(*modeStr) == "production" || strings.ToLower(*modeStr) == "prod" {
		mode = store.ModeProduction
	}

	log.Printf("==================================================")
	log.Printf("Starting OpAMP Management Server")
	log.Printf("Operating Mode: %s", mode)
	log.Printf("Port: %s", *port)
	log.Printf("==================================================")

	// 1. Initialize Base/Overlay Store
	var baseStore store.BaseOverlayStore
	var gitProvider store.GitProvider

	if *configMaps != "" {
		mounts := strings.Split(*configMaps, ",")
		for i := range mounts {
			mounts[i] = strings.TrimSpace(mounts[i])
		}
		log.Printf("[STORE] Using Kubernetes ConfigMaps: %v", mounts)
		baseStore = store.NewConfigMapStore(mounts)
	} else if *giteaURL != "" && *giteaOwner != "" && *giteaRepo != "" {
		log.Printf("[STORE] Using Gitea Provider at %s (%s/%s)", *giteaURL, *giteaOwner, *giteaRepo)
		giteaClient := store.NewGiteaClient(store.GiteaConfig{
			BaseURL: *giteaURL,
			Owner:   *giteaOwner,
			Repo:    *giteaRepo,
			Branch:  *giteaBranch,
			Token:   *giteaToken,
		})
		gitProvider = giteaClient
		baseStore = store.NewGitStore(giteaClient)
	} else {
		log.Printf("[STORE] Using Local Directory: %s", *configDir)
		baseStore = store.NewLocalStore(*configDir, mode)
	}

	// 2. Initialize Temporary Override Store (Redis / In-Memory)
	var redisStore *store.RedisOverrideStore
	if *redisAddr != "" {
		log.Printf("[REDIS] Connecting to Redis at %s", *redisAddr)
		redisStore = store.NewRedisOverrideStore(*redisAddr, *redisPassword, 0)
		if redisStore.IsInMemory() {
			log.Printf("[REDIS] Warning: Could not reach Redis at %s, falling back to In-Memory TTL store", *redisAddr)
		} else {
			log.Printf("[REDIS] Connected successfully to Redis")
		}
	} else {
		log.Printf("[REDIS] No Redis address specified. Using thread-safe in-memory TTL store")
		redisStore = store.NewInMemoryOverrideStore()
	}

	// 3. Combine Stores
	combinedStore := store.NewCombinedStore(baseStore, redisStore, mode, gitProvider)

	// 4. Initialize Audit Webhook Dispatcher
	auditPub := audit.NewDispatcher(audit.WebhookConfig{
		URL:    *webhookURL,
		Secret: *webhookSecret,
	})
	if *webhookURL != "" {
		log.Printf("[AUDIT] Webhook publishing enabled to: %s", *webhookURL)
	} else {
		log.Printf("[AUDIT] Webhook publishing disabled (in-memory audit logging active)")
	}

	// 5. Initialize OpAMP Protocol Server
	opampSrv, err := opamp.NewServer(combinedStore, auditPub, redisStore, *nodeID)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize OpAMP server: %v", err)
	}

	// 6. Initialize UI & REST API
	uiHandler := ui.NewHandler(combinedStore, opampSrv, auditPub)

	// 7. Configure HTTP Mux
	mux := http.NewServeMux()
	// OpAMP agent endpoint (both /v1/opamp and /opamp)
	mux.HandleFunc("/v1/opamp", opampSrv.HTTPHandler())
	mux.HandleFunc("/opamp", opampSrv.HTTPHandler())
	// Web UI & REST API
	mux.Handle("/", uiHandler)

	addr := ":" + *port
	httpSrv := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ConnContext: opampSrv.ConnContext(),
	}

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("OpAMP Server listening on http://0.0.0.0%s", addr)
		log.Printf("Web Dashboard available at http://localhost%s/", addr)
		log.Printf("OpAMP Agent WebSocket endpoint: ws://0.0.0.0%s/v1/opamp", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-stopChan
	log.Printf("Shutting down OpAMP server gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("Shutdown warning: %v", err)
	}
	log.Printf("Server stopped.")
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
