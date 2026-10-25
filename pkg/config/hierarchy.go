package config

import (
	"bytes"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// LayerRule defines a configurable overlay layer in the hierarchy
type LayerRule struct {
	Level       int      `json:"level" yaml:"level"`
	Name        string   `json:"name" yaml:"name"`
	Type        string   `json:"type,omitempty" yaml:"type,omitempty"` // "file" (default) or "redis"
	PathPattern string   `json:"path" yaml:"path"`                     // e.g. "environments/{{.Environment}}.yaml"
	Description string   `json:"description" yaml:"description"`
	Dimensions  []string `json:"dimensions,omitempty" yaml:"dimensions,omitempty"` // required non-empty dimensions
}

// HierarchyConfig represents hierarchy.yaml defining the complete overlay order
type HierarchyConfig struct {
	Layers []LayerRule `json:"layers" yaml:"layers"`
}

// DefaultHierarchy returns the standard 8-tier hierarchy
func DefaultHierarchy() *HierarchyConfig {
	return &HierarchyConfig{
		Layers: []LayerRule{
			{
				Level:       1,
				Name:        "Base",
				Type:        "file",
				PathPattern: "base.yaml",
				Description: "Global fleet defaults (Level 1 - Lowest Precedence)",
			},
			{
				Level:       2,
				Name:        "Environment",
				Type:        "file",
				PathPattern: "environments/{{.Environment}}.yaml",
				Description: "Environment settings (prod, staging, dev)",
				Dimensions:  []string{"Environment"},
			},
			{
				Level:       3,
				Name:        "Cluster",
				Type:        "file",
				PathPattern: "clusters/{{.Cluster}}.yaml",
				Description: "Kubernetes/cloud cluster settings",
				Dimensions:  []string{"Cluster"},
			},
			{
				Level:       4,
				Name:        "Subdomain",
				Type:        "file",
				PathPattern: "subdomains/{{.Subdomain}}.yaml",
				Description: "Network/routing subdomain settings",
				Dimensions:  []string{"Subdomain"},
			},
			{
				Level:       5,
				Name:        "Role",
				Type:        "file",
				PathPattern: "roles/{{.Role}}.yaml",
				Description: "Workload role settings (database, node-exporter, clickhouse)",
				Dimensions:  []string{"Role"},
			},
			{
				Level:       6,
				Name:        "Environment-Role",
				Type:        "file",
				PathPattern: "environments/{{.Environment}}/roles/{{.Role}}.yaml",
				Description: "Role settings specific to environment",
				Dimensions:  []string{"Environment", "Role"},
			},
			{
				Level:       7,
				Name:        "Hostname",
				Type:        "file",
				PathPattern: "hosts/{{.Hostname}}.yaml",
				Description: "Host-specific overrides",
				Dimensions:  []string{"Hostname"},
			},
			{
				Level:       8,
				Name:        "Redis-TTL",
				Type:        "redis",
				PathPattern: "redis:override:{{.Hostname}}",
				Description: "Temporary UI override with TTL (Level 8 - HIGHEST PRECEDENCE)",
				Dimensions:  []string{"Hostname"},
			},
		},
	}
}

// ParseHierarchy parses hierarchy.yaml YAML data
func ParseHierarchy(data []byte) (*HierarchyConfig, error) {
	var cfg HierarchyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// Assign levels sequentially if not specified
	for i := range cfg.Layers {
		if cfg.Layers[i].Level == 0 {
			cfg.Layers[i].Level = i + 1
		}
		if cfg.Layers[i].Type == "" {
			cfg.Layers[i].Type = "file"
		}
	}
	return &cfg, nil
}

// ResolvePath resolves path pattern using render context variables
func (r *LayerRule) ResolvePath(ctx RenderContext) (string, bool) {
	// Build map for template
	data := map[string]string{
		"Environment": ctx.Environment,
		"Cluster":     ctx.Cluster,
		"Subdomain":   ctx.Subdomain,
		"Role":        ctx.Role,
		"Hostname":    ctx.Hostname,
		"Region":      ctx.Region,
		"Cloud":       ctx.Cloud,
	}
	for k, v := range ctx.CustomAttributes {
		data[k] = v
	}

	// Check if all declared dimensions are non-empty
	for _, dim := range r.Dimensions {
		val := data[dim]
		if strings.TrimSpace(val) == "" {
			return "", false
		}
	}

	tmpl, err := template.New("path").Parse(r.PathPattern)
	if err != nil {
		return "", false
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", false
	}

	resolved := strings.TrimSpace(buf.String())
	// If path contains unresolved '<no value>' or empty slashes, reject
	if strings.Contains(resolved, "<no value>") || strings.Contains(resolved, "//") {
		return "", false
	}

	return resolved, true
}
