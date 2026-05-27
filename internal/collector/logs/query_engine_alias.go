// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"sort"
	"strings"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// ResolveStreamID accepts an alias (case-insensitive) or hex stream ID
// and returns the canonical hex ID. Hex hashes are tried first so the
// common case (already-resolved IDs) is O(1) without a lowercase
// conversion. Returns ("", false) when the input matches neither.
func (qe *QueryEngine) ResolveStreamID(input string) (string, bool) {
	if input == "" {
		return "", false
	}
	if _, ok := qe.streamByID[input]; ok {
		return input, true
	}
	if id, ok := qe.streamByAlias[strings.ToLower(input)]; ok {
		return id, true
	}
	return "", false
}

// AliasForStreamID returns the display-cased alias for the given stream
// hex ID. Empty string when the ID is unknown — callers should fall
// back to a hash-derived locator (e.g. the first 8 chars of the ID).
func (qe *QueryEngine) AliasForStreamID(id string) string {
	return qe.aliasByStreamID[id]
}

// ResolveTemplateID accepts an alias (case-insensitive) or hex template
// ID and returns the canonical hex ID. Same contract as
// ResolveStreamID.
func (qe *QueryEngine) ResolveTemplateID(input string) (string, bool) {
	if input == "" {
		return "", false
	}
	if _, ok := qe.templateByID[input]; ok {
		return input, true
	}
	if id, ok := qe.templateByAlias[strings.ToLower(input)]; ok {
		return id, true
	}
	return "", false
}

// AliasForTemplateID returns the display-cased alias for the given
// template hex ID. Empty string when the ID is unknown.
func (qe *QueryEngine) AliasForTemplateID(id string) string {
	return qe.aliasByTemplateID[id]
}

// streamAliasSuffixLen is the number of hex chars appended to a stream
// alias when collisions force disambiguation.
const streamAliasSuffixLen = 8

// templateAliasSuffixLen is the number of hex chars appended to a
// template alias when collisions force disambiguation. Templates use a
// shorter suffix because there are typically far fewer templates than
// streams in a graph and the severity suffix already carries
// information.
const templateAliasSuffixLen = 4

// assignStreamAliases iterates streams in deterministic ID order and
// produces the (lowercased→ID, ID→display) maps. When two streams
// share an alias the second occurrence (and onward) is suffixed with
// an 8-char slice of its ID — `pod.Unhealthy@a1b2c3d4`. The first
// occurrence keeps the unsuffixed alias so the most common-looking
// alias remains free of clutter.
func assignStreamAliases(streams []*wirelogs.LogStream) (map[string]string, map[string]string) {
	byAlias := make(map[string]string, len(streams))
	byID := make(map[string]string, len(streams))
	for _, s := range sortedStreamsByID(streams) {
		alias := s.Alias
		if alias == "" {
			alias = AliasFor(s)
		}
		if alias == "" {
			// Last-ditch: short hex prefix. Better than nothing for
			// the (degenerate) case of a stream with no labels.
			alias = ShortHash(s.ID)
		}
		key := strings.ToLower(alias)
		if _, taken := byAlias[key]; taken {
			alias = alias + "@" + ShortHash(s.ID)[:streamAliasSuffixLen]
			key = strings.ToLower(alias)
		}
		byAlias[key] = s.ID
		byID[s.ID] = alias
	}
	return byAlias, byID
}

// assignTemplateAliases is the template counterpart to
// assignStreamAliases. Template aliases use a 4-hex-char suffix on
// collision because templates are fewer and the alias already carries
// a severity tag (`@warn`, `@err`).
func assignTemplateAliases(templates []*wirelogs.LogTemplate) (map[string]string, map[string]string) {
	byAlias := make(map[string]string, len(templates))
	byID := make(map[string]string, len(templates))
	for _, t := range sortedTemplatesByID(templates) {
		alias := t.Alias
		if alias == "" {
			alias = TemplateAliasFor(t)
		}
		if alias == "" {
			alias = ShortHash(t.ID)
		}
		key := strings.ToLower(alias)
		if _, taken := byAlias[key]; taken {
			alias = alias + "-" + ShortHash(t.ID)[:templateAliasSuffixLen]
			key = strings.ToLower(alias)
		}
		byAlias[key] = t.ID
		byID[t.ID] = alias
	}
	return byAlias, byID
}

// sortedStreamsByID returns streams sorted by hex ID ascending. Sorting
// guarantees collision resolution is deterministic across runs
// regardless of Go map iteration order.
func sortedStreamsByID(streams []*wirelogs.LogStream) []*wirelogs.LogStream {
	out := make([]*wirelogs.LogStream, len(streams))
	copy(out, streams)
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// sortedTemplatesByID is the template counterpart to sortedStreamsByID.
func sortedTemplatesByID(templates []*wirelogs.LogTemplate) []*wirelogs.LogTemplate {
	out := make([]*wirelogs.LogTemplate, len(templates))
	copy(out, templates)
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}
