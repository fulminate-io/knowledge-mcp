// SPDX-License-Identifier: Apache-2.0
package config

import (
	"reflect"
	"strings"
)

// SettingsField describes one supported main-TOML setting. A wildcard is a
// named profile/family, never an arbitrary extension to the configuration.
type SettingsField struct {
	Path     []string `json:"path"`
	Type     string   `json:"type"`
	Secret   bool     `json:"secret,omitempty"`
	Readonly bool     `json:"readonly,omitempty"`
	Options  []any    `json:"options,omitempty"`
}

// SettingsCollection names a repeated or named group of settings the form
// renders as a list.
type SettingsCollection struct {
	Path        []string `json:"path"`
	Kind        string   `json:"kind"`
	NameOptions []string `json:"name_options,omitempty"`
}

// SettingsMetadata is the parser-derived descriptor set the Desktop settings
// form is built from.
type SettingsMetadata struct {
	Version     int                  `json:"version"`
	Fields      []SettingsField      `json:"fields"`
	Collections []SettingsCollection `json:"collections"`
	Notes       []string             `json:"notes"`
}

// MainSettingsMetadata derives scalar names/types from the parser's actual
// structs. Recursive fallback parsing is intentionally not a supported UI feature.
func MainSettingsMetadata() SettingsMetadata {
	out := SettingsMetadata{Version: 1, Notes: []string{
		"Embedding configuration and family assignments select creation-time defaults; existing graphs keep their recorded embedding identity.",
		"Named embedding profiles are complete configurations, not overrides of the default profile.",
		"Removing a saved key may reveal a credential or environment fallback; it does not delete environment variables.",
		"Account selection is managed by the account menu. Operational launch flags and collectors.json are separate configuration surfaces.",
		"An unset or nonpositive health_probe_interval uses the runtime default of 10 minutes.",
	}}
	root := reflect.TypeFor[parseShape]()
	for field := range root.Fields() {
		name := field.Tag.Get("toml")
		if name == "fulminate_account_id" {
			continue
		}
		typ := field.Type
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			out.Fields = append(out.Fields, settingsField([]string{name}, typ))
			continue
		}
		switch name {
		case "default", "summarizer", "supervisor", "topics":
			out.Fields = append(out.Fields, settingsScalarFields([]string{name}, reflect.TypeFor[parseSection]())...)
			if name != "default" {
				path := []string{name, "fallback"}
				out.Fields = append(out.Fields, SettingsField{Path: path, Type: "array"})
				out.Collections = append(out.Collections, SettingsCollection{Path: path, Kind: "ordered"})
			}
		case "credentials":
			out.Fields = append(out.Fields, settingsScalarFields([]string{name}, reflect.TypeFor[parseCredentials]())...)
		case "embedder":
			out.Fields = append(out.Fields, settingsScalarFields([]string{name}, reflect.TypeFor[parseEmbedSection]())...)
			out.Fields = append(out.Fields, settingsScalarFields([]string{name, "profile", "*"}, reflect.TypeFor[parseEmbedProfile]())...)
			out.Fields = append(out.Fields, settingsScalarFields([]string{name, "family", "*"}, reflect.TypeFor[parseEmbedFamily]())...)
			out.Collections = append(out.Collections, SettingsCollection{Path: []string{name, "profile"}, Kind: "named"}, SettingsCollection{Path: []string{name, "family"}, Kind: "named"})
		case "reranker":
			out.Fields = append(out.Fields, settingsScalarFields([]string{name}, reflect.TypeFor[parseRerankSection]())...)
		}
	}
	for i := range out.Collections {
		if len(out.Collections[i].Path) == 2 && out.Collections[i].Path[1] == "family" {
			for _, family := range AcceptedEmbedFamilies {
				out.Collections[i].NameOptions = append(out.Collections[i].NameOptions, family.String())
			}
		}
	}
	return out
}
func settingsScalarFields(prefix []string, typ reflect.Type) []SettingsField {
	var out []SettingsField
	for f := range typ.Fields() {
		if f.Type.Kind() == reflect.Map || f.Type.Kind() == reflect.Slice {
			continue
		}
		out = append(out, settingsField(append(append([]string{}, prefix...), f.Tag.Get("toml")), f.Type))
	}
	return out
}
func settingsField(path []string, typ reflect.Type) SettingsField {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	kind := "string"
	switch typ.Kind() {
	case reflect.Bool:
		kind = "boolean"
	case reflect.Int:
		kind = "integer"
	}
	out := SettingsField{Path: path, Type: kind}
	key := path[len(path)-1]
	out.Readonly = key == "schema_version"
	out.Secret = key == "key" || path[0] == "credentials"
	switch key {
	case "provider":
		if path[0] == "embedder" || path[0] == "reranker" {
			// EmbedProviderFake is DELIBERATELY ABSENT. It is a
			// deterministic test double that embeds nothing a search can
			// use, and a first-run user offered it in the same menu as
			// Voyage has no way to know that. It stays REPRESENTABLE:
			// EmbedProvider.IsValid accepts it (embed.go:48), so a stored
			// configuration naming it still parses and the form still
			// displays it as the current value through its
			// out-of-options menu item. Offered and displayed are
			// different questions and only the first is closed here.
			out.Options = []any{string(EmbedProviderVoyage), string(EmbedProviderCohere), string(EmbedProviderGemini), string(EmbedProviderOpenAICompatible)}
		} else {
			out.Options = []any{string(ProviderAnthropic), string(ProviderOpenAI), string(ProviderGemini), string(ProviderClaudeCLI), string(ProviderCodexCLI)}
		}
	case "dimension":
		for _, n := range AcceptedEmbedDimensions {
			out.Options = append(out.Options, n)
		}
	case "dtype":
		for _, s := range AcceptedEmbedDtypes {
			out.Options = append(out.Options, s)
		}
	}
	return out
}
func settingsPathMatch(pattern, path []string) bool {
	if len(pattern) != len(path) {
		return false
	}
	for i, s := range pattern {
		if s != "*" && s != path[i] {
			return false
		}
		if path[i] == "" || strings.ContainsAny(path[i], "\x00\r\n") {
			return false
		}
	}
	return true
}
