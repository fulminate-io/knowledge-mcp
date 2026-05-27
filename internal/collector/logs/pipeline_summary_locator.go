// SPDX-License-Identifier: Apache-2.0

package logs

import (
	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// templateLocator returns the alias-or-hash short form for a template
// summary line. Prefers Alias when present; falls back to TemplateAliasFor
// (recompute from pattern+severity), then to ShortHash so every entry is
// locatable regardless of when the graph was built.
func templateLocator(t *wirelogs.LogTemplate) string {
	if t == nil {
		return ""
	}
	if t.Alias != "" {
		return t.Alias
	}
	if alias := TemplateAliasFor(t); alias != "" {
		return alias
	}
	return ShortHash(t.ID)
}

// correlationLocator returns the alias-or-shortID locator for a template
// ID seen in a wirelogs.CorrelationResult. Falls back to the legacy 8-char-tail
// shortID rendering when the template isn't in the lookup map (e.g.
// correlations that reference a template the summary doesn't carry).
func correlationLocator(id string, lookup map[string]*wirelogs.LogTemplate) string {
	if t, ok := lookup[id]; ok && t != nil {
		if t.Alias != "" {
			return t.Alias
		}
		if alias := TemplateAliasFor(t); alias != "" {
			return alias
		}
	}
	return shortID(id)
}

// buildTemplateLookup indexes templates by ID for correlationLocator.
// Templates without an ID are skipped — they can't be referenced by a
// correlation entry anyway.
func buildTemplateLookup(templates []*wirelogs.LogTemplate) map[string]*wirelogs.LogTemplate {
	lookup := make(map[string]*wirelogs.LogTemplate, len(templates))
	for _, t := range templates {
		if t == nil || t.ID == "" {
			continue
		}
		lookup[t.ID] = t
	}
	return lookup
}
