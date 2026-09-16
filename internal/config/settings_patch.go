// SPDX-License-Identifier: Apache-2.0
package config

import (
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SettingsPatch is one typed change to a supported main-TOML path; Remove
// clears the path and is exclusive with Value.
type SettingsPatch struct {
	Path   []string        `json:"path"`
	Value  json.RawMessage `json:"value,omitempty"`
	Remove bool            `json:"remove,omitempty"`
}

// EditSettingsPatches applies typed changes to the existing document. Unknown
// and unrelated values (including keys) remain in the tree. Serialization is
// canonical TOML; callers commit only the fully parsed candidate atomically.
func EditSettingsPatches(data []byte, patches []SettingsPatch) ([]byte, error) {
	if _, err := Parse(data); err != nil {
		return nil, errors.New("configuration cannot be parsed")
	}
	if len(patches) == 0 || len(patches) > 1024 {
		return nil, errors.New("invalid settings patch")
	}
	var tree map[string]any
	if toml.Unmarshal(data, &tree) != nil {
		return nil, errors.New("configuration cannot be parsed")
	}
	if tree == nil {
		tree = map[string]any{}
	}
	schema := MainSettingsMetadata()
	for _, patch := range patches {
		value, err := decodeSettingsPatch(schema, patch)
		if err != nil {
			return nil, err
		}
		if err := applySettingsPatch(tree, patch, value); err != nil {
			return nil, err
		}
	}
	out, err := toml.Marshal(tree)
	if err != nil {
		return nil, errors.New("settings cannot be encoded")
	}
	if _, err := Parse(out); err != nil {
		return nil, errors.New("edited configuration is invalid")
	}
	return out, nil
}
func decodeSettingsPatch(schema SettingsMetadata, patch SettingsPatch) (any, error) {
	if len(patch.Path) == 0 || len(patch.Path) > 4 || patch.Remove && len(patch.Value) != 0 {
		return nil, errors.New("invalid settings patch")
	}
	for _, part := range patch.Path {
		if part == "" || strings.ContainsAny(part, "\x00\r\n") {
			return nil, errors.New("invalid settings path")
		}
	}
	var field *SettingsField
	for i := range schema.Fields {
		if settingsPathMatch(schema.Fields[i].Path, patch.Path) {
			field = &schema.Fields[i]
			break
		}
	}
	if field == nil {
		named := len(patch.Path) == 3 && patch.Path[0] == "embedder" && (patch.Path[1] == "profile" || patch.Path[1] == "family")
		if named && patch.Remove {
			return nil, nil
		}
		return nil, errors.New("unsupported settings path")
	}
	if field.Readonly {
		return nil, errors.New("read-only setting")
	}
	if patch.Remove {
		return nil, nil
	}
	var value any
	if len(patch.Value) == 0 || json.Unmarshal(patch.Value, &value) != nil || !validSettingsValue(*field, value) {
		return nil, errors.New("invalid setting type or value")
	}
	if field.Type == "integer" {
		number, ok := value.(float64)
		if !ok {
			return nil, errors.New("invalid integer value")
		}
		value = int64(number)
	}
	return value, nil
}
func applySettingsPatch(tree map[string]any, patch SettingsPatch, value any) error {
	target := tree
	for _, part := range patch.Path[:len(patch.Path)-1] {
		child, exists := target[part]
		if !exists {
			if patch.Remove {
				return nil
			}
			child = map[string]any{}
			target[part] = child
		}
		next, ok := child.(map[string]any)
		if !ok {
			return errors.New("setting parent is not a table")
		}
		target = next
	}
	key := patch.Path[len(patch.Path)-1]
	if patch.Remove {
		delete(target, key)
	} else {
		target[key] = value
	}
	return nil
}
func validSettingsValue(field SettingsField, value any) bool {
	switch field.Type {
	case "string":
		s, ok := value.(string)
		return ok && len(s) <= 16384
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		n, ok := value.(float64)
		return ok && math.Trunc(n) == n && n >= 0 && n <= 1<<31-1
	case "array":
		entries, ok := value.([]any)
		if !ok || len(entries) > 100 {
			return false
		}
		for _, entry := range entries {
			fields, ok := entry.(map[string]any)
			if !ok {
				return false
			}
			for k, v := range fields {
				if k != "provider" && k != "model" && k != "cli_bin" && k != "base_url" {
					return false
				}
				if s, ok := v.(string); !ok || len(s) > 16384 {
					return false
				}
			}
		}
		return true
	}
	return false
}
