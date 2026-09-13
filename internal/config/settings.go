// SPDX-License-Identifier: Apache-2.0
package config

import (
	"bytes"
	"errors"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// EditSettings changes only requested scalar values, preserving the surrounding
// TOML, comments, unknown keys and unrelated tables. Parse gates both versions.
func EditSettings(data []byte, edits map[string]map[string]string) ([]byte, error) {
	if _, err := Parse(data); err != nil {
		return nil, errors.New("configuration cannot be parsed")
	}
	out := bytes.Clone(data)
	sections := make([]string, 0, len(edits))
	for section := range edits {
		sections = append(sections, section)
	}
	sort.Strings(sections)
	for _, section := range sections {
		keys := make([]string, 0, len(edits[section]))
		for key := range edits[section] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			encoded, err := toml.Marshal(map[string]string{"value": edits[section][key]})
			if err != nil {
				return nil, errors.New("setting cannot be encoded")
			}
			value := bytes.TrimSpace(bytes.SplitN(encoded, []byte("="), 2)[1])
			out, err = editSetting(out, section, key, value)
			if err != nil {
				return nil, err
			}
		}
	}
	if _, err := Parse(out); err != nil {
		return nil, errors.New("edited configuration is invalid")
	}
	return out, nil
}
func settingKey(n *unstable.Node) []string {
	var keys []string
	it := n.Key()
	for it.Next() {
		keys = append(keys, string(it.Node().Data))
	}
	return keys
}
func editSetting(data []byte, section, key string, value []byte) ([]byte, error) {
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	insert := -1
	firstHeader := len(data)
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			start := int(n.Raw.Offset)
			if start < firstHeader {
				firstHeader = start
			}
			if len(table) == 1 && table[0] == section && insert < 0 {
				insert = start
			}
			table = settingKey(n)
			if n.Kind == unstable.ArrayTable {
				table = append(table, "[]")
			}
			continue
		}
		if n.Kind != unstable.KeyValue {
			continue
		}
		full := append(append([]string{}, table...), settingKey(n)...)
		if len(full) == 1 && full[0] == section && n.Value().Kind == unstable.InlineTable {
			return editInlineSetting(data, n.Value(), key, value), nil
		}
		if len(full) == 2 && full[0] == section && full[1] == key {
			r := n.Value().Raw
			start, end := int(r.Offset), int(r.Offset+r.Length)
			out := append(bytes.Clone(data[:start]), value...)
			return append(out, data[end:]...), nil
		}
	}
	if parser.Error() != nil {
		return nil, errors.New("configuration cannot be parsed")
	}
	if len(table) == 1 && table[0] == section && insert < 0 {
		insert = len(data)
	}
	assignment := key + " = " + string(value) + "\n"
	if insert >= 0 {
		return append(append(bytes.Clone(data[:insert]), []byte("\n"+assignment)...), data[insert:]...), nil
	}
	// A dotted assignment before the first header avoids changing the meaning of
	// any existing table. Parse refuses incompatible inline/dotted definitions.
	assignment = "\n" + strings.Join([]string{section, key}, ".") + " = " + string(value) + "\n"
	return append(append(bytes.Clone(data[:firstHeader]), []byte(assignment)...), data[firstHeader:]...), nil
}

func editInlineSetting(data []byte, inline *unstable.Node, key string, value []byte) []byte {
	children := inline.Children()
	hasChildren := false
	for children.Next() {
		hasChildren = true
		entry := children.Node()
		keys := settingKey(entry)
		if len(keys) == 1 && keys[0] == key {
			r := entry.Value().Raw
			start, end := int(r.Offset), int(r.Offset+r.Length)
			out := append(bytes.Clone(data[:start]), value...)
			return append(out, data[end:]...)
		}
	}
	start := int(inline.Raw.Offset + inline.Raw.Length)
	addition := key + " = " + string(value)
	if hasChildren {
		addition += ", "
	}
	return append(append(bytes.Clone(data[:start]), []byte(addition)...), data[start:]...)
}
