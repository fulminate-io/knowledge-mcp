// SPDX-License-Identifier: Apache-2.0
package config

import (
	"errors"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SettingsSecret reports whether a credential is configured, present from any
// source, and where it comes from; never the value.
type SettingsSecret struct {
	Path       []string `json:"path"`
	Configured bool     `json:"configured"`
	Present    bool     `json:"present"`
	Source     string   `json:"source"`
}

// SettingsDocument is the public settings view: schema, configured values,
// effective values and credential presence.
type SettingsDocument struct {
	Schema        SettingsMetadata `json:"schema"`
	Configuration map[string]any   `json:"configuration"`
	Effective     map[string]any   `json:"effective"`
	SecretStatus  []SettingsSecret `json:"secret_status"`
}

// ReadSettingsDocument projects only supported values. Credential material and
// unknown extension fields never enter the public representation.
func ReadSettingsDocument(data []byte) (*SettingsDocument, error) {
	c, err := Parse(data)
	if err != nil {
		return nil, errors.New("configuration cannot be parsed")
	}
	var raw map[string]any
	if toml.Unmarshal(data, &raw) != nil {
		return nil, errors.New("configuration cannot be parsed")
	}
	out := &SettingsDocument{Schema: MainSettingsMetadata(), Configuration: map[string]any{}, Effective: map[string]any{}, SecretStatus: []SettingsSecret{}}
	projectConfiguredSettings(out, c, raw)
	out.Effective["schema_version"] = c.SchemaVersion
	out.Effective["auto_update"] = c.AutoUpdate == nil || *c.AutoUpdate
	interval := "10m0s"
	if c.HealthProbeInterval > 0 {
		interval = c.HealthProbeInterval.String()
	}
	out.Effective["health_probe_interval"] = interval
	out.Effective["default"] = publicLLM(c.Default)
	for _, name := range []Consumer{ConsumerSummarizer, ConsumerSupervisor, ConsumerTopics} {
		chain, err := c.ResolveChain(name)
		if err != nil {
			continue
		}
		primary := publicLLM(chain[0])
		fallback := make([]any, 0, len(chain)-1)
		for _, entry := range chain[1:] {
			fallback = append(fallback, publicLLM(entry))
		}
		primary["fallback"] = fallback
		out.Effective[string(name)] = primary
	}
	embed, err := c.ResolveEmbedder()
	if err != nil {
		return nil, errors.New("invalid embedding configuration")
	}
	resolved := publicEmbed(embed)
	profiles := map[string]any{}
	for name, section := range c.EmbedProfiles {
		profiles[name] = publicEmbed(section)
	}
	resolved["profile"] = profiles
	families := map[string]any{}
	for _, family := range AcceptedEmbedFamilies {
		profile, err := c.ResolveEmbedProfileForFamily(family.String())
		if err != nil {
			return nil, errors.New("invalid family assignment")
		}
		families[family.String()] = map[string]any{"profile": profile.Name}
	}
	resolved["family"] = families
	out.Effective["embedder"] = resolved
	rank, err := c.ResolveReranker()
	if err != nil {
		return nil, errors.New("invalid reranker configuration")
	}
	out.Effective["reranker"] = map[string]any{"provider": string(rank.Provider), "model": rank.Model, "base_url": rank.BaseURL}
	return out, nil
}
func projectConfiguredSettings(out *SettingsDocument, c *Config, raw map[string]any) {
	for _, field := range out.Schema.Fields {
		for _, path := range concreteSettingsPaths(raw, field.Path) {
			value, exists := settingsLookup(raw, path)
			if field.Secret {
				text, _ := value.(string)
				source := settingsSecretFallback(c, raw, path)
				if text != "" {
					source = "saved"
				}
				out.SecretStatus = append(out.SecretStatus, SettingsSecret{Path: path, Configured: exists, Present: source != "missing", Source: source})
			} else if exists {
				if field.Type == "array" {
					value = publicFallbacks(value)
				}
				settingsPut(out.Configuration, path, value)
			}
		}
	}
}
func publicLLM(s Section) map[string]any {
	return map[string]any{"provider": string(s.Provider), "model": s.Model, "cli_bin": s.CLIBin, "base_url": s.BaseURL}
}
func publicEmbed(s EmbedSection) map[string]any {
	return map[string]any{"provider": string(s.Provider), "model": s.Model, "base_url": s.BaseURL, "dimension": s.Dimension, "dtype": s.Dtype}
}
func publicFallbacks(value any) any {
	entries, ok := value.([]any)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]any{}
		for _, key := range []string{"provider", "model", "cli_bin", "base_url"} {
			if v, exists := fields[key]; exists {
				row[key] = v
			}
		}
		out = append(out, row)
	}
	return out
}
func settingsLookup(tree map[string]any, path []string) (any, bool) {
	for i, key := range path {
		value, ok := tree[key]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			return value, true
		}
		tree, ok = value.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return nil, false
}
func settingsPut(tree map[string]any, path []string, value any) {
	for _, key := range path[:len(path)-1] {
		child, ok := tree[key].(map[string]any)
		if !ok {
			child = map[string]any{}
			tree[key] = child
		}
		tree = child
	}
	tree[path[len(path)-1]] = value
}
func concreteSettingsPaths(tree map[string]any, pattern []string) [][]string {
	for i, part := range pattern {
		if part == "*" {
			value, _ := settingsLookup(tree, pattern[:i])
			named, _ := value.(map[string]any)
			out := make([][]string, 0, len(named))
			for _, name := range sortedKeys(named) {
				path := append([]string{}, pattern...)
				path[i] = name
				out = append(out, path)
			}
			return out
		}
	}
	return [][]string{pattern}
}
func settingsSecretFallback(c *Config, raw map[string]any, path []string) string {
	credential := path[len(path)-1]
	if path[0] != "credentials" {
		provider := settingsSecretProvider(c, path)
		switch provider {
		case EmbedProviderVoyage:
			credential = "voyage_api_key"
		case EmbedProviderCohere:
			credential = "cohere_api_key"
		case EmbedProviderGemini:
			credential = "gemini_api_key"
		case EmbedProviderOpenAICompatible:
			credential = "openai_api_key"
		default:
			return "missing"
		}
		value, _ := settingsLookup(raw, []string{"credentials", credential})
		if text, ok := value.(string); ok && text != "" {
			return "credentials"
		}
	}
	if os.Getenv(strings.ToUpper(credential)) != "" {
		return "environment"
	}
	return "missing"
}

func settingsSecretProvider(c *Config, path []string) EmbedProvider {
	if path[0] == "reranker" {
		s, _ := c.ResolveReranker()
		return s.Provider
	}
	if len(path) == 4 {
		return c.EmbedProfiles[path[2]].Provider
	}
	s, _ := c.ResolveEmbedder()
	return s.Provider
}
