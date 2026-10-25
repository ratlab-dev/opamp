package ui

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/naugustin/opamp-server/pkg/audit"
	"github.com/naugustin/opamp-server/pkg/config"
	"github.com/naugustin/opamp-server/pkg/opamp"
	"github.com/naugustin/opamp-server/pkg/store"
	"gopkg.in/yaml.v3"
)

//go:embed static/*
var staticFS embed.FS

// Handler manages UI assets and REST API endpoints
type Handler struct {
	appStore    store.AppStore
	opampServer *opamp.Server
	auditPub    *audit.Dispatcher
	mux         *http.ServeMux
}

func NewHandler(appStore store.AppStore, opampServer *opamp.Server, auditPub *audit.Dispatcher) *Handler {
	h := &Handler{
		appStore:    appStore,
		opampServer: opampServer,
		auditPub:    auditPub,
		mux:         http.NewServeMux(),
	}
	h.registerRoutes()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) registerRoutes() {
	// Static frontend
	h.mux.HandleFunc("/", h.handleIndex)

	// API endpoints
	h.mux.HandleFunc("/api/status", h.handleStatus)
	h.mux.HandleFunc("/api/agents", h.handleAgents)
	h.mux.HandleFunc("/api/agent/config", h.handleAgentConfig)
	h.mux.HandleFunc("/api/targets", h.handleTargets)
	h.mux.HandleFunc("/api/overrides/impact", h.handleOverrideImpact)
	h.mux.HandleFunc("/api/overrides/preview", h.handleOverridePreview)
	h.mux.HandleFunc("/api/render", h.handleRender)
	h.mux.HandleFunc("/api/overrides", h.handleOverrides)
	h.mux.HandleFunc("/api/store/tree", h.handleStoreTree)
	h.mux.HandleFunc("/api/store/file", h.handleStoreFile)
	h.mux.HandleFunc("/api/config/validate", h.handleValidateConfig)
	h.mux.HandleFunc("/api/config/save", h.handleSaveConfig)
	h.mux.HandleFunc("/api/audit", h.handleAudit)
	h.mux.HandleFunc("/api/events", h.opampServer.Broker().ServeSSE)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		// Serve embedded static asset or 404
		data, err := staticFS.ReadFile("static" + r.URL.Path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Content-Type", "text/css")
		} else if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "application/javascript")
		}
		_, _ = w.Write(data)
		return
	}

	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "UI template missing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	overrides, _ := h.appStore.ListOverrides()
	agents := h.opampServer.Registry().ListAgents()

	resp := map[string]interface{}{
		"mode":             h.appStore.Mode(),
		"connected_agents": len(agents),
		"active_overrides": len(overrides),
		"time_utc":         time.Now().UTC().Format(time.RFC3339),
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (h *Handler) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents := h.opampServer.Registry().ListAgents()
	jsonResponse(w, http.StatusOK, agents)
}

func (h *Handler) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		http.Error(w, "Missing uid parameter", http.StatusBadRequest)
		return
	}

	agentInfo, found := h.opampServer.Registry().GetAgent(uid)
	if !found {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	inSync := (agentInfo.ReportedConfigYAML != "" && agentInfo.EffectiveConfigYAML != "" && agentInfo.ReportedConfigYAML == agentInfo.EffectiveConfigYAML) ||
		(agentInfo.LastRemoteHash != "" && agentInfo.EffectiveHash != "" && agentInfo.LastRemoteHash == agentInfo.EffectiveHash)

	resp := map[string]interface{}{
		"instance_uid":          agentInfo.InstanceUID,
		"hostname":              agentInfo.Hostname,
		"environment":           agentInfo.Environment,
		"role":                  agentInfo.Role,
		"cluster":               agentInfo.Cluster,
		"status":                agentInfo.Status,
		"reported_config_yaml":  agentInfo.ReportedConfigYAML,
		"effective_config_yaml": agentInfo.EffectiveConfigYAML,
		"last_remote_hash":      agentInfo.LastRemoteHash,
		"effective_hash":        agentInfo.EffectiveHash,
		"in_sync":               inSync,
	}
	jsonResponse(w, http.StatusOK, resp)
}

type TargetScopeOption struct {
	Type         string         `json:"type"`          // "host", "role", "environment", "cluster", "subdomain", "env_role"
	Value        string         `json:"value"`         // e.g. "database", "prod", "prod-db-01"
	TargetKey    string         `json:"target_key"`    // e.g. "role:database", "environment:prod", "host:prod-db-01"
	Label        string         `json:"label"`         // e.g. "Role: database"
	TotalHosts   int            `json:"total_hosts"`
	Environments map[string]int `json:"environments"` // env -> count
	Roles        map[string]int `json:"roles"`        // role -> count
}

type ImpactReport struct {
	Target           string              `json:"target"`
	ScopeType        string              `json:"scope_type"`
	ScopeValue       string              `json:"scope_value"`
	ScopeLevelName   string              `json:"scope_level_name"`
	TotalHosts       int                 `json:"total_hosts"`
	ByEnvironment    map[string]int      `json:"by_environment"`
	ByRole           map[string]int      `json:"by_role"`
	Hosts            []ImpactHostSummary `json:"hosts"`
	CurrentStateYAML string              `json:"current_state_yaml"`
}

type ImpactHostSummary struct {
	Hostname    string `json:"hostname"`
	Environment string `json:"environment"`
	Role        string `json:"role"`
	Cluster     string `json:"cluster"`
}

func (h *Handler) handleTargets(w http.ResponseWriter, r *http.Request) {
	agents := h.opampServer.Registry().ListAgents()

	// Sets of discovered values
	knownHosts := make(map[string]bool)
	knownRoles := map[string]bool{"mac-work": true, "mac-dev": true, "database": true, "node-exporter": true, "clickhouse": true}
	knownEnvs := map[string]bool{"prod": true, "staging": true, "dev": true}
	knownClusters := map[string]bool{"us-east-1": true}
	knownSubdomains := make(map[string]bool)
	knownEnvRoles := make(map[string]bool)

	for _, a := range agents {
		if a.Hostname != "" {
			knownHosts[a.Hostname] = true
		}
		if a.Role != "" {
			knownRoles[a.Role] = true
		}
		if a.Environment != "" {
			knownEnvs[a.Environment] = true
		}
		if a.Cluster != "" {
			knownClusters[a.Cluster] = true
		}
		if a.Subdomain != "" {
			knownSubdomains[a.Subdomain] = true
		}
		if a.Environment != "" && a.Role != "" {
			knownEnvRoles[a.Environment+":"+a.Role] = true
		}
	}

	var options []TargetScopeOption

	// Add roles
	for role := range knownRoles {
		opt := TargetScopeOption{
			Type:         "role",
			Value:        role,
			TargetKey:    "role:" + role,
			Label:        "Role: " + role,
			Environments: make(map[string]int),
			Roles:        map[string]int{role: 0},
		}
		for _, a := range agents {
			if a.Role == role {
				opt.TotalHosts++
				if a.Environment != "" {
					opt.Environments[a.Environment]++
				}
				opt.Roles[role]++
			}
		}
		options = append(options, opt)
	}

	// Add environments
	for env := range knownEnvs {
		opt := TargetScopeOption{
			Type:         "environment",
			Value:        env,
			TargetKey:    "environment:" + env,
			Label:        "Environment: " + env,
			Environments: map[string]int{env: 0},
			Roles:        make(map[string]int),
		}
		for _, a := range agents {
			if a.Environment == env {
				opt.TotalHosts++
				opt.Environments[env]++
				if a.Role != "" {
					opt.Roles[a.Role]++
				}
			}
		}
		options = append(options, opt)
	}

	// Add clusters
	for cluster := range knownClusters {
		opt := TargetScopeOption{
			Type:         "cluster",
			Value:        cluster,
			TargetKey:    "cluster:" + cluster,
			Label:        "Cluster: " + cluster,
			Environments: make(map[string]int),
			Roles:        make(map[string]int),
		}
		for _, a := range agents {
			if a.Cluster == cluster {
				opt.TotalHosts++
				if a.Environment != "" {
					opt.Environments[a.Environment]++
				}
				if a.Role != "" {
					opt.Roles[a.Role]++
				}
			}
		}
		options = append(options, opt)
	}

	// Add subdomains
	if len(knownSubdomains) == 0 {
		knownSubdomains["payments"] = true
		knownSubdomains["auth"] = true
		knownSubdomains["analytics"] = true
	}
	for sub := range knownSubdomains {
		opt := TargetScopeOption{
			Type:         "subdomain",
			Value:        sub,
			TargetKey:    "subdomain:" + sub,
			Label:        "Subdomain: " + sub,
			Environments: make(map[string]int),
			Roles:        make(map[string]int),
		}
		for _, a := range agents {
			if a.Subdomain == sub {
				opt.TotalHosts++
				if a.Environment != "" {
					opt.Environments[a.Environment]++
				}
				if a.Role != "" {
					opt.Roles[a.Role]++
				}
			}
		}
		options = append(options, opt)
	}

	// Add hosts
	if len(knownHosts) == 0 {
		knownHosts["prod-db-01"] = true
	}
	for host := range knownHosts {
		opt := TargetScopeOption{
			Type:         "host",
			Value:        host,
			TargetKey:    "host:" + host,
			Label:        "Host: " + host,
			Environments: make(map[string]int),
			Roles:        make(map[string]int),
		}
		for _, a := range agents {
			if a.Hostname == host {
				opt.TotalHosts = 1
				if a.Environment != "" {
					opt.Environments[a.Environment]++
				}
				if a.Role != "" {
					opt.Roles[a.Role]++
				}
			}
		}
		options = append(options, opt)
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"targets": options})
}

type ImpactRequest struct {
	Target string `json:"target"` // e.g. "role:database", "environment:prod", "host:prod-db-01"
}

func (h *Handler) handleOverrideImpact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ImpactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	target := strings.TrimSpace(req.Target)
	scopeType := "host"
	scopeValue := target

	if idx := strings.Index(target, ":"); idx != -1 {
		scopeType = target[:idx]
		scopeValue = target[idx+1:]
	}

	agents := h.opampServer.Registry().ListAgents()
	report := ImpactReport{
		Target:        target,
		ScopeType:     scopeType,
		ScopeValue:    scopeValue,
		ByEnvironment: make(map[string]int),
		ByRole:        make(map[string]int),
		Hosts:         make([]ImpactHostSummary, 0),
	}

	for _, a := range agents {
		match := false
		switch scopeType {
		case "role":
			match = (a.Role == scopeValue)
		case "environment", "env":
			match = (a.Environment == scopeValue)
		case "cluster":
			match = (a.Cluster == scopeValue)
		case "subdomain":
			match = (a.Subdomain == scopeValue)
		case "env_role":
			parts := strings.Split(scopeValue, ":")
			if len(parts) == 2 {
				match = (a.Environment == parts[0] && a.Role == parts[1])
			}
		case "host":
			match = (a.Hostname == scopeValue || a.InstanceUID == scopeValue)
		default:
			match = (a.Hostname == scopeValue || a.InstanceUID == scopeValue)
		}

		if match {
			report.TotalHosts++
			if a.Environment != "" {
				report.ByEnvironment[a.Environment]++
			}
			if a.Role != "" {
				report.ByRole[a.Role]++
			}
			report.Hosts = append(report.Hosts, ImpactHostSummary{
				Hostname:    a.Hostname,
				Environment: a.Environment,
				Role:        a.Role,
				Cluster:     a.Cluster,
			})
		}
	}

	// Fetch current state for this target entity (layer overlay or active Redis override)
	var layerCurrent map[string]interface{}
	scopeLevelName := strings.Title(scopeType) + " Scope"
	switch scopeType {
	case "environment", "env":
		layerCurrent, _ = h.appStore.GetEnv(scopeValue)
		scopeLevelName = "Environment (" + scopeValue + ")"
	case "cluster":
		layerCurrent, _ = h.appStore.GetCluster(scopeValue)
		scopeLevelName = "Cluster (" + scopeValue + ")"
	case "subdomain":
		layerCurrent, _ = h.appStore.GetSubdomain(scopeValue)
		scopeLevelName = "Subdomain (" + scopeValue + ")"
	case "role":
		layerCurrent, _ = h.appStore.GetRole(scopeValue)
		scopeLevelName = "Role (" + scopeValue + ")"
	case "env_role":
		parts := strings.Split(scopeValue, ":")
		if len(parts) == 2 {
			layerCurrent, _ = h.appStore.GetEnvRole(parts[0], parts[1])
		}
		scopeLevelName = "Env-Role (" + scopeValue + ")"
	case "host":
		layerCurrent, _ = h.appStore.GetHost(scopeValue)
		scopeLevelName = "Host (" + scopeValue + ")"
	}

	// Check if there is already an active Redis override for this target
	activeRedis, _ := h.appStore.GetRedisOverride(target)
	if len(activeRedis) > 0 {
		layerCurrent = activeRedis
	}

	var curStateYAML string
	if len(layerCurrent) > 0 {
		b, err := yaml.Marshal(layerCurrent)
		if err == nil && len(b) > 0 && string(b) != "{}\n" {
			curStateYAML = string(b)
		}
	} else if scopeType == "host" {
		// If no dedicated host overlay file exists yet, render the effective merged config for this host
		renderCtx := config.RenderContext{Hostname: scopeValue}
		if len(report.Hosts) > 0 {
			renderCtx.Environment = report.Hosts[0].Environment
			renderCtx.Role = report.Hosts[0].Role
			renderCtx.Cluster = report.Hosts[0].Cluster
		}
		effBytes, _, err := config.Render(h.appStore, renderCtx)
		if err == nil && len(effBytes) > 0 {
			curStateYAML = string(effBytes)
		}
	}

	report.ScopeLevelName = scopeLevelName
	report.CurrentStateYAML = curStateYAML

	jsonResponse(w, http.StatusOK, report)
}

type OverridePreviewRequest struct {
	Target     string `json:"target"`
	ConfigYAML string `json:"config_yaml"`
}

type OverridePreviewResponse struct {
	Target         string `json:"target"`
	ScopeType      string `json:"scope_type"`
	ScopeValue     string `json:"scope_value"`
	ScopeLevelName string `json:"scope_level_name"`
	SampleHost     string `json:"sample_host,omitempty"`
	TotalHosts     int    `json:"total_hosts"`

	// Layer-level state (what the configuration at this specific layer / level looks like before and after)
	LayerCurrentYAML  string `json:"layer_current_yaml"`
	LayerProposedYAML string `json:"layer_proposed_yaml"`

	// Effective merged state (what the full merged configuration looks like for the target or sample host before and after)
	EffectiveCurrentYAML  string `json:"effective_current_yaml"`
	EffectiveProposedYAML string `json:"effective_proposed_yaml"`
	CurrentHash           string `json:"current_hash"`
	ProposedHash          string `json:"proposed_hash"`
}

// previewStore intercepts calls for a temporary target override to compute proposed effective config
type previewStore struct {
	store.AppStore
	mockTarget string
	mockConfig map[string]interface{}
}

func (p *previewStore) GetRedisOverride(target string) (map[string]interface{}, error) {
	if target == p.mockTarget || strings.TrimPrefix(target, "host:") == strings.TrimPrefix(p.mockTarget, "host:") {
		return p.mockConfig, nil
	}
	return p.AppStore.GetRedisOverride(target)
}

func (h *Handler) handleOverridePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req OverridePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	target := strings.TrimSpace(req.Target)
	if target == "" {
		http.Error(w, "Target cannot be empty", http.StatusBadRequest)
		return
	}

	var parsedOverlay map[string]interface{}
	if err := yaml.Unmarshal([]byte(req.ConfigYAML), &parsedOverlay); err != nil {
		http.Error(w, "Invalid YAML format: "+err.Error(), http.StatusBadRequest)
		return
	}

	scopeType := "host"
	scopeValue := target
	if idx := strings.Index(target, ":"); idx != -1 {
		scopeType = target[:idx]
		scopeValue = target[idx+1:]
	}

	agents := h.opampServer.Registry().ListAgents()
	var matchingAgents []*opamp.AgentInfo
	for _, a := range agents {
		match := false
		switch scopeType {
		case "role":
			match = (a.Role == scopeValue)
		case "environment", "env":
			match = (a.Environment == scopeValue)
		case "cluster":
			match = (a.Cluster == scopeValue)
		case "subdomain":
			match = (a.Subdomain == scopeValue)
		case "env_role":
			parts := strings.Split(scopeValue, ":")
			if len(parts) == 2 {
				match = (a.Environment == parts[0] && a.Role == parts[1])
			}
		case "host":
			match = (a.Hostname == scopeValue || a.InstanceUID == scopeValue)
		default:
			match = (a.Hostname == scopeValue || a.InstanceUID == scopeValue)
		}
		if match {
			matchingAgents = append(matchingAgents, a)
		}
	}

	// 1. Fetch current layer config for this scope
	var layerCurrent map[string]interface{}
	scopeLevelName := strings.Title(scopeType) + " Scope"
	switch scopeType {
	case "environment", "env":
		layerCurrent, _ = h.appStore.GetEnv(scopeValue)
		scopeLevelName = "Environment (" + scopeValue + ")"
	case "cluster":
		layerCurrent, _ = h.appStore.GetCluster(scopeValue)
		scopeLevelName = "Cluster (" + scopeValue + ")"
	case "subdomain":
		layerCurrent, _ = h.appStore.GetSubdomain(scopeValue)
		scopeLevelName = "Subdomain (" + scopeValue + ")"
	case "role":
		layerCurrent, _ = h.appStore.GetRole(scopeValue)
		scopeLevelName = "Role (" + scopeValue + ")"
	case "env_role":
		parts := strings.Split(scopeValue, ":")
		if len(parts) == 2 {
			layerCurrent, _ = h.appStore.GetEnvRole(parts[0], parts[1])
		}
		scopeLevelName = "Env-Role (" + scopeValue + ")"
	case "host":
		layerCurrent, _ = h.appStore.GetHost(scopeValue)
		scopeLevelName = "Host (" + scopeValue + ")"
	}

	// Also check if there's already an active Redis override for this target
	activeRedis, _ := h.appStore.GetRedisOverride(target)
	if len(activeRedis) > 0 {
		layerCurrent = activeRedis
	}

	layerCurBytes, _ := yaml.Marshal(layerCurrent)
	if len(layerCurBytes) == 0 || string(layerCurBytes) == "{}\n" {
		layerCurBytes = []byte("# No prior overlay defined for this layer\n")
	}

	// Merge overlay on top of layer current
	layerProposed := make(map[string]interface{})
	if len(layerCurrent) > 0 {
		config.DeepMergeYAML(&layerProposed, layerCurrent)
	}
	config.DeepMergeYAML(&layerProposed, parsedOverlay)
	layerPropBytes, _ := yaml.Marshal(layerProposed)

	// 2. Compute effective current & proposed on a representative RenderContext
	ctx := config.RenderContext{}
	sampleHost := ""
	if len(matchingAgents) > 0 {
		ctx = config.RenderContext{
			Environment: matchingAgents[0].Environment,
			Cluster:     matchingAgents[0].Cluster,
			Subdomain:   matchingAgents[0].Subdomain,
			Role:        matchingAgents[0].Role,
			Hostname:    matchingAgents[0].Hostname,
		}
		sampleHost = matchingAgents[0].Hostname
	} else {
		// Populate from scope
		switch scopeType {
		case "environment", "env":
			ctx.Environment = scopeValue
		case "cluster":
			ctx.Cluster = scopeValue
		case "subdomain":
			ctx.Subdomain = scopeValue
		case "role":
			ctx.Role = scopeValue
		case "host":
			ctx.Hostname = scopeValue
			sampleHost = scopeValue
		case "env_role":
			parts := strings.Split(scopeValue, ":")
			if len(parts) == 2 {
				ctx.Environment = parts[0]
				ctx.Role = parts[1]
			}
		}
	}

	// Current effective config
	effCurBytes, curHash, _ := config.Render(h.appStore, ctx)

	// Proposed effective config using intercepted store
	pStore := &previewStore{
		AppStore:   h.appStore,
		mockTarget: target,
		mockConfig: parsedOverlay,
	}
	effPropBytes, propHash, _ := config.Render(pStore, ctx)

	resp := OverridePreviewResponse{
		Target:                target,
		ScopeType:             scopeType,
		ScopeValue:            scopeValue,
		ScopeLevelName:        scopeLevelName,
		SampleHost:            sampleHost,
		TotalHosts:            len(matchingAgents),
		LayerCurrentYAML:      string(layerCurBytes),
		LayerProposedYAML:     string(layerPropBytes),
		EffectiveCurrentYAML:  string(effCurBytes),
		EffectiveProposedYAML: string(effPropBytes),
		CurrentHash:           curHash,
		ProposedHash:          propHash,
	}

	jsonResponse(w, http.StatusOK, resp)
}


type RenderRequest struct {
	Environment string `json:"environment"`
	Cluster     string `json:"cluster"`
	Subdomain   string `json:"subdomain"`
	Role        string `json:"role"`
	Hostname    string `json:"hostname"`
}

func (h *Handler) handleRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RenderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := config.RenderContext{
		Environment: req.Environment,
		Cluster:     req.Cluster,
		Subdomain:   req.Subdomain,
		Role:        req.Role,
		Hostname:    req.Hostname,
	}

	yamlBytes, hashStr, prov, err := config.RenderWithProvenance(h.appStore, ctx)
	if err != nil {
		http.Error(w, "Rendering failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Build GitOps Guidance for various scopes
	gitGuide := h.buildGitGuide(ctx)

	resp := map[string]interface{}{
		"yaml":        string(yamlBytes),
		"hash":        hashStr,
		"provenance":  prov,
		"git_guide":   gitGuide,
		"server_mode": h.appStore.Mode(),
	}
	jsonResponse(w, http.StatusOK, resp)
}

type GitGuideOption struct {
	Scope       string `json:"scope"`
	FilePath    string `json:"file_path"`
	Description string `json:"description"`
	EditURL     string `json:"edit_url,omitempty"`
	SampleGit   string `json:"sample_git"`
}

func (h *Handler) buildGitGuide(ctx config.RenderContext) []GitGuideOption {
	var options []GitGuideOption

	// Base
	options = append(options, GitGuideOption{
		Scope:       "Global Base (Fleet-wide)",
		FilePath:    "base.yaml",
		Description: "Affects every collector across all environments and roles.",
		EditURL:     h.appStore.GetGitEditURL("base", ""),
		SampleGit:   "git checkout -b update-base-config\n# edit base.yaml\ngit commit -am 'feat(otel): update base config'\ngit push origin update-base-config",
	})

	if ctx.Environment != "" {
		options = append(options, GitGuideOption{
			Scope:       "Environment (" + ctx.Environment + ")",
			FilePath:    "environments/" + ctx.Environment + ".yaml",
			Description: "Affects all collectors in the " + ctx.Environment + " environment.",
			EditURL:     h.appStore.GetGitEditURL("environments", ctx.Environment),
			SampleGit:   fmt.Sprintf("git checkout -b update-%s-config\n# edit environments/%s.yaml\ngit commit -am 'feat(otel): update %s environment config'\ngit push origin update-%s-config", ctx.Environment, ctx.Environment, ctx.Environment, ctx.Environment),
		})
	}

	if ctx.Role != "" {
		options = append(options, GitGuideOption{
			Scope:       "Role (" + ctx.Role + ")",
			FilePath:    "roles/" + ctx.Role + ".yaml",
			Description: "Affects all collectors running the " + ctx.Role + " role workload.",
			EditURL:     h.appStore.GetGitEditURL("roles", ctx.Role),
			SampleGit:   fmt.Sprintf("git checkout -b update-%s-role\n# edit roles/%s.yaml\ngit commit -am 'feat(otel): update %s role config'\ngit push origin update-%s-role", ctx.Role, ctx.Role, ctx.Role, ctx.Role),
		})
	}

	if ctx.Cluster != "" {
		options = append(options, GitGuideOption{
			Scope:       "Cluster (" + ctx.Cluster + ")",
			FilePath:    "clusters/" + ctx.Cluster + ".yaml",
			Description: "Affects all collectors inside cluster " + ctx.Cluster + ".",
			EditURL:     h.appStore.GetGitEditURL("clusters", ctx.Cluster),
			SampleGit:   fmt.Sprintf("git checkout -b update-%s-cluster\n# edit clusters/%s.yaml\ngit commit -am 'feat(otel): update %s cluster config'\ngit push origin update-%s-cluster", ctx.Cluster, ctx.Cluster, ctx.Cluster, ctx.Cluster),
		})
	}

	if ctx.Hostname != "" {
		options = append(options, GitGuideOption{
			Scope:       "Host (" + ctx.Hostname + ")",
			FilePath:    "hosts/" + ctx.Hostname + ".yaml",
			Description: "Affects only host " + ctx.Hostname + " permanently.",
			EditURL:     h.appStore.GetGitEditURL("hosts", ctx.Hostname),
			SampleGit:   fmt.Sprintf("git checkout -b update-%s-host\n# edit hosts/%s.yaml\ngit commit -am 'feat(otel): update host %s config'\ngit push origin update-%s-host", ctx.Hostname, ctx.Hostname, ctx.Hostname, ctx.Hostname),
		})
	}

	return options
}

type SetOverrideRequest struct {
	Target     string `json:"target"`
	ConfigYAML string `json:"config_yaml"`
	TTLSeconds int64  `json:"ttl_seconds"`
	Duration   string `json:"duration,omitempty"` // e.g. "1h", "6h", "12h", "24h"
	Author     string `json:"author"`
	Reason     string `json:"reason"`
}

func (h *Handler) handleOverrides(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		overrides, err := h.appStore.ListOverrides()
		if err != nil {
			http.Error(w, "Failed to list overrides: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, overrides)

	case http.MethodPost:
		var req SetOverrideRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(req.Target) == "" {
			http.Error(w, "Target cannot be empty", http.StatusBadRequest)
			return
		}

		// Calculate TTL duration
		ttl := time.Duration(req.TTLSeconds) * time.Second
		if req.Duration != "" {
			d, err := time.ParseDuration(req.Duration)
			if err == nil && d > 0 {
				ttl = d
			}
		}
		if ttl <= 0 {
			ttl = 6 * time.Hour // default 6h
		}

		// Validate YAML
		var parsed map[string]interface{}
		if err := yaml.Unmarshal([]byte(req.ConfigYAML), &parsed); err != nil {
			http.Error(w, "Invalid YAML format: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Author == "" {
			req.Author = r.RemoteAddr
		}
		if req.Reason == "" {
			req.Reason = "Temporary operational override via UI"
		}

		meta := store.OverrideMetadata{
			Target: req.Target,
			Author: req.Author,
			Reason: req.Reason,
		}

		if err := h.appStore.SetOverride(req.Target, parsed, ttl, meta); err != nil {
			http.Error(w, "Failed to persist override: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Trigger audit webhook
		if h.auditPub != nil {
			h.auditPub.PublishAsync(audit.NewEvent(
				audit.EventTemporaryOverrideCreated,
				"create_temporary_override",
				audit.Actor{Identity: req.Author, IPAddress: r.RemoteAddr},
				audit.Target{Hostname: req.Target, Path: "redis:override:" + req.Target},
				audit.EventDetails{
					TTLSeconds:  int64(ttl.Seconds()),
					ExpiresAt:   time.Now().Add(ttl).UTC().Format(time.RFC3339),
					Reason:      req.Reason,
					DiffSnippet: req.ConfigYAML,
				},
			))
		}

		// Actively recheck and push to connected agents
		h.opampServer.BroadcastConfigCheck(r.Context())

		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"success":     true,
			"target":      req.Target,
			"ttl_seconds": int64(ttl.Seconds()),
			"expires_at":  time.Now().Add(ttl).UTC().Format(time.RFC3339),
		})

	case http.MethodDelete:
		target := r.URL.Query().Get("target")
		if target == "" {
			http.Error(w, "Missing target query param", http.StatusBadRequest)
			return
		}

		if err := h.appStore.DeleteOverride(target); err != nil {
			http.Error(w, "Failed to delete override: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Trigger audit webhook
		if h.auditPub != nil {
			h.auditPub.PublishAsync(audit.NewEvent(
				audit.EventTemporaryOverrideDeleted,
				"delete_temporary_override",
				audit.Actor{Identity: "operator", IPAddress: r.RemoteAddr},
				audit.Target{Hostname: target, Path: "redis:override:" + target},
				audit.EventDetails{Reason: "Revoked temporary override early from UI"},
			))
		}

		// Recheck configs
		h.opampServer.BroadcastConfigCheck(r.Context())

		jsonResponse(w, http.StatusOK, map[string]interface{}{"success": true, "deleted": target})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

type SaveConfigRequest struct {
	Layer      string `json:"layer"`
	Name       string `json:"name"`
	ConfigYAML string `json:"config_yaml"`
}

func (h *Handler) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.appStore.Mode() == store.ModeProduction {
		http.Error(w, "Configuration writes are prohibited in production mode. Use GitOps PR or temporary Redis overrides.", http.StatusForbidden)
		return
	}

	var req SaveConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.appStore.WriteConfig(req.Layer, req.Name, []byte(req.ConfigYAML)); err != nil {
		http.Error(w, "Failed to write file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Audit log
	if h.auditPub != nil {
		h.auditPub.PublishAsync(audit.NewEvent(
			audit.EventConfigModified,
			"write_local_config",
			audit.Actor{Identity: "developer", IPAddress: r.RemoteAddr},
			audit.Target{Path: req.Layer + "/" + req.Name + ".yaml"},
			audit.EventDetails{Reason: "Saved config in development mode"},
		))
	}

	// Recheck connected agents
	h.opampServer.BroadcastConfigCheck(r.Context())

	jsonResponse(w, http.StatusOK, map[string]interface{}{"success": true})
}

type ValidateConfigRequest struct {
	YAML string `json:"yaml"`
}

type LintError struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
	Type    string `json:"type"` // "error", "warning"
}

type ValidateConfigResponse struct {
	Valid  bool        `json:"valid"`
	Errors []LintError `json:"errors"`
}

func (h *Handler) handleValidateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ValidateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var parsed map[string]interface{}
	err := yaml.Unmarshal([]byte(req.YAML), &parsed)
	if err == nil {
		jsonResponse(w, http.StatusOK, ValidateConfigResponse{
			Valid:  true,
			Errors: []LintError{},
		})
		return
	}

	// Parse yaml error line & message if possible
	line := 1
	col := 1
	msg := err.Error()

	// yaml.v3 error strings typically contain "yaml: line X: ..."
	if strings.Contains(msg, "line ") {
		parts := strings.Split(msg, "line ")
		if len(parts) > 1 {
			sub := parts[1]
			colonIdx := strings.Index(sub, ":")
			if colonIdx != -1 {
				lineNumStr := strings.TrimSpace(sub[:colonIdx])
				if n, numErr := strconv.Atoi(lineNumStr); numErr == nil {
					line = n
				}
				msg = strings.TrimSpace(sub[colonIdx+1:])
			}
		}
	}

	jsonResponse(w, http.StatusOK, ValidateConfigResponse{
		Valid: false,
		Errors: []LintError{
			{
				Line:    line,
				Column:  col,
				Message: msg,
				Type:    "error",
			},
		},
	})
}

// ConfigFileNode represents a file or directory node in the Config Store Explorer
type ConfigFileNode struct {
	Name            string            `json:"name"`
	Path            string            `json:"path"`             // relative path e.g. "roles/database.yaml"
	Type            string            `json:"type"`             // "file" or "directory"
	Layer           string            `json:"layer,omitempty"` // "base", "environments", "roles", etc.
	TargetScope     string            `json:"target_scope,omitempty"` // e.g. "role:database"
	HasOverride     bool              `json:"has_override"`
	OverrideMeta    *store.OverrideMetadata `json:"override_meta,omitempty"`
	OverrideYAML    string            `json:"override_yaml,omitempty"`
	Children        []*ConfigFileNode `json:"children,omitempty"`
}

type StoreTreeResponse struct {
	Mode         string            `json:"mode"`
	RootDir      string            `json:"root_dir"`
	Tree         []*ConfigFileNode `json:"tree"`
	OverrideCount int              `json:"override_count"`
}

func (h *Handler) handleStoreTree(w http.ResponseWriter, r *http.Request) {
	overrides, _ := h.appStore.ListOverrides()
	overrideMap := make(map[string]store.OverrideMetadata)
	for _, o := range overrides {
		overrideMap[o.Target] = o
	}

	// Determine root directory if local store
	rootDir := ""
	if cs, ok := h.appStore.(*store.CombinedStore); ok {
		if ls, ok := cs.BaseOverlayStore().(*store.LocalStore); ok {
			rootDir = ls.RootDir()
		}
	}

	var rootNodes []*ConfigFileNode
	if rootDir != "" {
		rootNodes = h.buildFileSystemTree(rootDir, "", overrideMap)
	} else {
		// Fallback virtual tree from known layers
		rootNodes = h.buildVirtualTree(overrideMap)
	}

	jsonResponse(w, http.StatusOK, StoreTreeResponse{
		Mode:          string(h.appStore.Mode()),
		RootDir:       rootDir,
		Tree:          rootNodes,
		OverrideCount: len(overrides),
	})
}

func (h *Handler) buildFileSystemTree(rootDir, relPath string, overrideMap map[string]store.OverrideMetadata) []*ConfigFileNode {
	fullPath := filepath.Join(rootDir, relPath)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return []*ConfigFileNode{}
	}

	var nodes []*ConfigFileNode
	for _, entry := range entries {
		// Skip hidden files/directories (like .git)
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		childRel := filepath.Join(relPath, entry.Name())
		if relPath == "" {
			childRel = entry.Name()
		}

		if entry.IsDir() {
			children := h.buildFileSystemTree(rootDir, childRel, overrideMap)
			// Check if directory level itself has an override (e.g. environment:prod)
			dirNode := &ConfigFileNode{
				Name:     entry.Name(),
				Path:     childRel,
				Type:     "directory",
				Layer:    entry.Name(),
				Children: children,
			}
			nodes = append(nodes, dirNode)
		} else {
			// Only include yaml/yml files
			ext := filepath.Ext(entry.Name())
			if ext != ".yaml" && ext != ".yml" {
				continue
			}

			layer, targetScope := determineLayerAndScope(childRel)
			fileNode := &ConfigFileNode{
				Name:        entry.Name(),
				Path:        childRel,
				Type:        "file",
				Layer:       layer,
				TargetScope: targetScope,
			}

			// Check if Redis has an override matching this target scope
			if targetScope != "" {
				if ov, found := overrideMap[targetScope]; found {
					fileNode.HasOverride = true
					fileNode.OverrideMeta = &ov
					fileNode.OverrideYAML = ov.ConfigYAML
				}
			}

			nodes = append(nodes, fileNode)
		}
	}
	return nodes
}

func (h *Handler) buildVirtualTree(overrideMap map[string]store.OverrideMetadata) []*ConfigFileNode {
	layers := []string{"base", "environments", "clusters", "subdomains", "roles", "hosts"}
	var nodes []*ConfigFileNode

	for _, l := range layers {
		dirNode := &ConfigFileNode{
			Name:  l,
			Path:  l,
			Type:  "directory",
			Layer: l,
		}
		nodes = append(nodes, dirNode)
	}
	return nodes
}

func determineLayerAndScope(relPath string) (string, string) {
	clean := filepath.Clean(relPath)
	if clean == "base.yaml" || clean == "base.yml" {
		return "base", ""
	}
	if clean == "hierarchy.yaml" || clean == "hierarchy.yml" {
		return "hierarchy", ""
	}

	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) >= 2 {
		layer := parts[0]
		name := strings.TrimSuffix(parts[len(parts)-1], filepath.Ext(parts[len(parts)-1]))

		switch layer {
		case "environments", "environment":
			return "environments", "environment:" + name
		case "clusters", "cluster":
			return "clusters", "cluster:" + name
		case "subdomains", "subdomain":
			return "subdomains", "subdomain:" + name
		case "roles", "role":
			return "roles", "role:" + name
		case "hosts", "host":
			return "hosts", "host:" + name
		}
	}
	return "custom", ""
}

type StoreFileResponse struct {
	Path         string                  `json:"path"`
	Layer        string                  `json:"layer"`
	TargetScope  string                  `json:"target_scope"`
	ContentYAML  string                  `json:"content_yaml"`
	HasOverride  bool                    `json:"has_override"`
	OverrideMeta *store.OverrideMetadata `json:"override_meta,omitempty"`
	OverrideYAML string                  `json:"override_yaml,omitempty"`
	MergedYAML   string                  `json:"merged_yaml,omitempty"`
	Mode         string                  `json:"mode"`
	CanEdit      bool                    `json:"can_edit"`
}

func (h *Handler) handleStoreFile(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "Missing path parameter", http.StatusBadRequest)
		return
	}

	// Security: prevent directory traversal
	cleanPath := filepath.Clean(relPath)
	if strings.Contains(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	var contentBytes []byte
	var err error

	// If local store, read file directly
	if cs, ok := h.appStore.(*store.CombinedStore); ok {
		if ls, ok := cs.BaseOverlayStore().(*store.LocalStore); ok {
			fullPath := filepath.Join(ls.RootDir(), cleanPath)
			contentBytes, err = os.ReadFile(fullPath)
		}
	}

	if err != nil {
		// Try GetByPath
		if cfg, pErr := h.appStore.GetByPath(cleanPath); pErr == nil && cfg != nil {
			contentBytes, _ = yaml.Marshal(cfg)
		} else {
			http.Error(w, "File not found: "+err.Error(), http.StatusNotFound)
			return
		}
	}

	layer, targetScope := determineLayerAndScope(cleanPath)
	resp := StoreFileResponse{
		Path:        cleanPath,
		Layer:       layer,
		TargetScope: targetScope,
		ContentYAML: string(contentBytes),
		Mode:        string(h.appStore.Mode()),
		CanEdit:     (h.appStore.Mode() == store.ModeDevelopment),
	}

	// Check if this level has an active Redis override
	if targetScope != "" {
		if ov, err := h.appStore.GetOverride(targetScope); err == nil && len(ov) > 0 {
			resp.HasOverride = true
			ovBytes, _ := yaml.Marshal(ov)
			resp.OverrideYAML = string(ovBytes)

			// Also fetch metadata
			overrides, _ := h.appStore.ListOverrides()
			for _, m := range overrides {
				if m.Target == targetScope {
					resp.OverrideMeta = &m
					break
				}
			}

			// Compute merged YAML for this file layer + redis override
			var baseParsed map[string]interface{}
			_ = yaml.Unmarshal(contentBytes, &baseParsed)
			merged := make(map[string]interface{})
			if len(baseParsed) > 0 {
				config.DeepMergeYAML(&merged, baseParsed)
			}
			config.DeepMergeYAML(&merged, ov)
			mBytes, _ := yaml.Marshal(merged)
			resp.MergedYAML = string(mBytes)
		}
	}

	jsonResponse(w, http.StatusOK, resp)
}


func (h *Handler) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	events := h.auditPub.GetRecentEvents(limit)
	jsonResponse(w, http.StatusOK, events)
}

func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
