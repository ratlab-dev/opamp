package config

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PrecedenceLayer details an overlay layer and its status in the evaluated hierarchy
type PrecedenceLayer struct {
	Level       int                    `json:"level"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Source      string                 `json:"source"`
	Active      bool                   `json:"active"`
	Config      map[string]interface{} `json:"config,omitempty"`
}

// ProvenanceReport details the layers evaluated and merged to create the effective configuration
type ProvenanceReport struct {
	Context RenderContext     `json:"context"`
	Layers  []PrecedenceLayer `json:"layers"`
	Hash    string            `json:"hash"`
}

// RenderContext holds the identifying parameters of the target collector
type RenderContext struct {
	Environment      string            `json:"environment"`
	Cluster          string            `json:"cluster"`
	Subdomain        string            `json:"subdomain"`
	Role             string            `json:"role"`
	Hostname         string            `json:"hostname"`
	Region           string            `json:"region,omitempty"`
	Cloud            string            `json:"cloud,omitempty"`
	CustomAttributes map[string]string `json:"custom_attributes,omitempty"`
}

// ConfigStore is an interface that abstracts where YAML configs come from
type ConfigStore interface {
	GetBase() (map[string]interface{}, error)
	GetEnv(env string) (map[string]interface{}, error)
	GetCluster(cluster string) (map[string]interface{}, error)
	GetSubdomain(subdomain string) (map[string]interface{}, error)
	GetRole(role string) (map[string]interface{}, error)
	GetHost(hostname string) (map[string]interface{}, error)
	GetRedisOverride(target string) (map[string]interface{}, error)
}

// EnvRoleStore is an optional interface extension for environment-specific role overrides
type EnvRoleStore interface {
	GetEnvRole(env, role string) (map[string]interface{}, error)
}

// HierarchyStore allows retrieving a custom hierarchy.yaml from the store
type HierarchyStore interface {
	GetHierarchy() (*HierarchyConfig, error)
	GetByPath(path string) (map[string]interface{}, error)
}

// Render merges configurations based on precedence rules and returns deterministically hashed YAML
func Render(store ConfigStore, ctx RenderContext) ([]byte, string, error) {
	yamlBytes, hash, _, err := RenderWithProvenance(store, ctx)
	return yamlBytes, hash, err
}

// RenderWithProvenance merges configurations and returns a layer-by-layer provenance breakdown
func RenderWithProvenance(store ConfigStore, ctx RenderContext) ([]byte, string, *ProvenanceReport, error) {
	finalConfig := make(map[string]interface{})
	var layers []PrecedenceLayer

	// Check if store provides a custom hierarchy.yaml definition
	var hierarchy *HierarchyConfig
	if hStore, ok := store.(HierarchyStore); ok {
		if customH, err := hStore.GetHierarchy(); err == nil && customH != nil && len(customH.Layers) > 0 {
			hierarchy = customH
		}
	}
	if hierarchy == nil {
		hierarchy = DefaultHierarchy()
	}

	// Evaluate layers in strict order defined by hierarchy
	for _, rule := range hierarchy.Layers {
		layer := PrecedenceLayer{
			Level:       rule.Level,
			Name:        rule.Name,
			Description: rule.Description,
			Active:      false,
		}

		resolvedPath, ok := rule.ResolvePath(ctx)
		if !ok {
			layer.Source = rule.PathPattern
			layers = append(layers, layer)
			continue
		}
		layer.Source = resolvedPath

		var cfg map[string]interface{}
		var err error

		if rule.Type == "redis" {
			// Redis temporary overrides - evaluate in order from broad scope to host-specific scope
			type candidate struct {
				name   string
				source string
				keys   []string
			}
			var candidates []candidate

			if ctx.Environment != "" {
				candidates = append(candidates, candidate{
					name:   "Redis-TTL-Environment",
					source: "redis:override:environment:" + ctx.Environment,
					keys:   []string{"environment:" + ctx.Environment, "env:" + ctx.Environment},
				})
			}
			if ctx.Cluster != "" {
				candidates = append(candidates, candidate{
					name:   "Redis-TTL-Cluster",
					source: "redis:override:cluster:" + ctx.Cluster,
					keys:   []string{"cluster:" + ctx.Cluster},
				})
			}
			if ctx.Subdomain != "" {
				candidates = append(candidates, candidate{
					name:   "Redis-TTL-Subdomain",
					source: "redis:override:subdomain:" + ctx.Subdomain,
					keys:   []string{"subdomain:" + ctx.Subdomain},
				})
			}
			if ctx.Role != "" {
				candidates = append(candidates, candidate{
					name:   "Redis-TTL-Role",
					source: "redis:override:role:" + ctx.Role,
					keys:   []string{"role:" + ctx.Role},
				})
			}
			if ctx.Environment != "" && ctx.Role != "" {
				candidates = append(candidates, candidate{
					name:   "Redis-TTL-EnvRole",
					source: "redis:override:env_role:" + ctx.Environment + ":" + ctx.Role,
					keys:   []string{"env_role:" + ctx.Environment + ":" + ctx.Role},
				})
			}
			if ctx.Hostname != "" {
				candidates = append(candidates, candidate{
					name:   "Redis-TTL-Host",
					source: "redis:override:host:" + ctx.Hostname,
					keys:   []string{"host:" + ctx.Hostname, ctx.Hostname},
				})
			}

			redisApplied := false
			for _, cand := range candidates {
				var candCfg map[string]interface{}
				for _, k := range cand.keys {
					c, err := store.GetRedisOverride(k)
					if err == nil && len(c) > 0 {
						candCfg = c
						break
					}
				}
				if len(candCfg) > 0 {
					redisApplied = true
					candLayer := PrecedenceLayer{
						Level:       rule.Level,
						Name:        cand.name,
						Description: "Temporary UI override in Redis (" + cand.source + ")",
						Source:      cand.source,
						Active:      true,
						Config:      candCfg,
					}
					DeepMergeYAML(&finalConfig, candCfg)
					layers = append(layers, candLayer)
				}
			}

			if !redisApplied {
				layer.Source = resolvedPath
				layers = append(layers, layer)
			}
			continue
		}

		// File overlay - try path query or fall back to standard getters
		cfg, err = fetchLayerConfig(store, ctx, rule, resolvedPath)
		if err == nil && len(cfg) > 0 {
			layer.Active = true
			layer.Config = cfg
			DeepMergeYAML(&finalConfig, cfg)
		}
		layers = append(layers, layer)
	}

	// Deterministic YAML marshaling (yaml.v3 sorts map keys alphabetically)
	yamlBytes, err := yaml.Marshal(finalConfig)
	if err != nil {
		return nil, "", nil, err
	}

	hashBytes := sha256.Sum256(yamlBytes)
	hashStr := hex.EncodeToString(hashBytes[:])

	report := &ProvenanceReport{
		Context: ctx,
		Layers:  layers,
		Hash:    hashStr,
	}

	return yamlBytes, hashStr, report, nil
}

// fetchLayerConfig fetches configuration using specific methods or fallback path resolution
func fetchLayerConfig(store ConfigStore, ctx RenderContext, rule LayerRule, resolvedPath string) (map[string]interface{}, error) {
	// If store supports GetByPath, use it directly
	if hStore, ok := store.(HierarchyStore); ok {
		cfg, err := hStore.GetByPath(resolvedPath)
		if err == nil && cfg != nil {
			return cfg, nil
		}
	}

	// Fallback to standard typed getters
	clean := filepath.Clean(resolvedPath)
	switch {
	case clean == "base.yaml" || clean == "base.yml":
		return store.GetBase()
	case strings.HasPrefix(clean, "environments/") && strings.Contains(clean, "/roles/"):
		if erStore, ok := store.(EnvRoleStore); ok {
			return erStore.GetEnvRole(ctx.Environment, ctx.Role)
		}
	case strings.HasPrefix(clean, "environments/"):
		return store.GetEnv(ctx.Environment)
	case strings.HasPrefix(clean, "clusters/"):
		return store.GetCluster(ctx.Cluster)
	case strings.HasPrefix(clean, "subdomains/"):
		return store.GetSubdomain(ctx.Subdomain)
	case strings.HasPrefix(clean, "roles/"):
		return store.GetRole(ctx.Role)
	case strings.HasPrefix(clean, "hosts/"):
		return store.GetHost(ctx.Hostname)
	}

	return nil, nil
}

// DeepMergeYAML deeply merges src into dst with override semantics for maps and deduplication for lists
func DeepMergeYAML(dst *map[string]interface{}, src map[string]interface{}) {
	if *dst == nil {
		*dst = make(map[string]interface{})
	}
	for k, srcVal := range src {
		dstVal, exists := (*dst)[k]
		if !exists {
			(*dst)[k] = deepCopy(srcVal)
			continue
		}

		// Both are maps -> recurse
		srcMap, srcIsMap := srcVal.(map[string]interface{})
		dstMap, dstIsMap := dstVal.(map[string]interface{})
		if srcIsMap && dstIsMap {
			DeepMergeYAML(&dstMap, srcMap)
			(*dst)[k] = dstMap
			continue
		}

		// Both are slices -> merge with deduplication
		srcSlice, srcIsSlice := srcVal.([]interface{})
		dstSlice, dstIsSlice := dstVal.([]interface{})
		if srcIsSlice && dstIsSlice {
			merged := mergeSlices(dstSlice, srcSlice)
			(*dst)[k] = merged
			continue
		}

		// Otherwise scalar value -> overlay overrides base
		(*dst)[k] = deepCopy(srcVal)
	}
}

func mergeSlices(dst, src []interface{}) []interface{} {
	result := make([]interface{}, len(dst))
	copy(result, dst)

	// Set of existing primitive values to prevent duplicates
	seen := make(map[interface{}]bool)
	for _, item := range dst {
		if isPrimitive(item) {
			seen[item] = true
		}
	}

	for _, item := range src {
		if isPrimitive(item) {
			if !seen[item] {
				seen[item] = true
				result = append(result, item)
			}
		} else {
			// For non-primitives (maps/structs), check if there is an item with matching name/job_name
			matched := false
			if srcMap, ok := item.(map[string]interface{}); ok {
				srcKey := getIdentifierKey(srcMap)
				if srcKey != "" {
					for idx, existing := range result {
						if existingMap, ok := existing.(map[string]interface{}); ok {
							if getIdentifierKey(existingMap) == srcKey {
								DeepMergeYAML(&existingMap, srcMap)
								result[idx] = existingMap
								matched = true
								break
							}
						}
					}
				}
			}
			if !matched {
				result = append(result, deepCopy(item))
			}
		}
	}
	return result
}

func getIdentifierKey(m map[string]interface{}) string {
	if v, ok := m["name"].(string); ok && v != "" {
		return "name:" + v
	}
	if v, ok := m["job_name"].(string); ok && v != "" {
		return "job_name:" + v
	}
	if v, ok := m["id"].(string); ok && v != "" {
		return "id:" + v
	}
	return ""
}

func isPrimitive(v interface{}) bool {
	switch v.(type) {
	case string, int, int64, float64, bool:
		return true
	default:
		return false
	}
}

func deepCopy(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		cp := make(map[string]interface{}, len(val))
		for k, item := range val {
			cp[k] = deepCopy(item)
		}
		return cp
	case []interface{}:
		cp := make([]interface{}, len(val))
		for i, item := range val {
			cp[i] = deepCopy(item)
		}
		return cp
	default:
		return val
	}
}
