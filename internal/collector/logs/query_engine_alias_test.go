// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"strings"
	"testing"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// aliasEngineStreams returns three streams whose derived aliases are
// distinct, used to verify the happy-path alias map population.
func aliasEngineStreams() []*wirelogs.LogStream {
	return []*wirelogs.LogStream{
		{
			ID:    "00aa11bb22cc33dd44ee55ff66778899aabbccddeeff00112233445566778899",
			Alias: "api-7b6.OOMKilled",
			Labels: map[string]string{
				"pod_name": "api-7b6",
				"reason":   "OOMKilled",
			},
		},
		{
			ID:     "11aa11bb22cc33dd44ee55ff66778899aabbccddeeff00112233445566778899",
			Alias:  "checkout@host-3",
			Labels: map[string]string{"app": "checkout", "instance": "host-3"},
		},
		{
			ID:     "22aa11bb22cc33dd44ee55ff66778899aabbccddeeff00112233445566778899",
			Alias:  "worker-1.NodeNotReady",
			Labels: map[string]string{"pod_name": "worker-1", "reason": "NodeNotReady"},
		},
	}
}

func aliasEngineTemplates() []*wirelogs.LogTemplate {
	return []*wirelogs.LogTemplate{
		{ID: "ta00000000000000000000000000000000000000000000000000000000000001",
			Pattern: "Connection refused", Severity: wirelogs.SeverityError, Alias: "connection-refused@err"},
		{ID: "ta00000000000000000000000000000000000000000000000000000000000002",
			Pattern: "Node not ready", Severity: wirelogs.SeverityWarn, Alias: "node-not-ready@warn"},
	}
}

func TestQueryEngine_StreamAliasPopulated(t *testing.T) {
	streams := aliasEngineStreams()
	qe := NewQueryEngine(streams, nil, aliasEngineTemplates())
	for _, s := range streams {
		got := qe.AliasForStreamID(s.ID)
		if got == "" {
			t.Errorf("AliasForStreamID(%q) is empty", s.ID)
		}
	}
}

func TestQueryEngine_StreamAliasCollisionSuffix(t *testing.T) {
	// Two K8s streams that distill to the same `pod.reason` form when
	// the pod_name happens to repeat across namespaces. The engine must
	// disambiguate the second one with an 8-char hash suffix.
	a := &wirelogs.LogStream{
		ID:    "aa00000000000000000000000000000000000000000000000000000000000001",
		Alias: "api.OOMKilled",
		Labels: map[string]string{
			"pod_name":  "api",
			"reason":    "OOMKilled",
			"namespace": "prod",
		},
	}
	b := &wirelogs.LogStream{
		ID:    "bb00000000000000000000000000000000000000000000000000000000000002",
		Alias: "api.OOMKilled",
		Labels: map[string]string{
			"pod_name":  "api",
			"reason":    "OOMKilled",
			"namespace": "staging",
		},
	}
	qe := NewQueryEngine([]*wirelogs.LogStream{a, b}, nil, nil)
	aliasA := qe.AliasForStreamID(a.ID)
	aliasB := qe.AliasForStreamID(b.ID)
	if aliasA == aliasB {
		t.Fatalf("collision not resolved: both aliases = %q", aliasA)
	}
	// First-by-sorted-ID keeps the unsuffixed alias.
	if aliasA != "api.OOMKilled" {
		t.Errorf("first-by-ID alias = %q, want api.OOMKilled", aliasA)
	}
	if !strings.HasPrefix(aliasB, "api.OOMKilled@") {
		t.Errorf("second alias = %q, want prefix api.OOMKilled@", aliasB)
	}
	// Suffix is 8 hex chars taken from the colliding stream's ID.
	suffix := strings.TrimPrefix(aliasB, "api.OOMKilled@")
	if suffix != b.ID[:8] {
		t.Errorf("collision suffix = %q, want %q", suffix, b.ID[:8])
	}
}

func TestQueryEngine_ResolveStreamID_Alias(t *testing.T) {
	streams := aliasEngineStreams()
	qe := NewQueryEngine(streams, nil, aliasEngineTemplates())

	// Alias → hex.
	id, ok := qe.ResolveStreamID("api-7b6.OOMKilled")
	if !ok || id != streams[0].ID {
		t.Errorf("ResolveStreamID(alias) = (%q, %v), want (%q, true)", id, ok, streams[0].ID)
	}

	// Hex passthrough.
	id, ok = qe.ResolveStreamID(streams[1].ID)
	if !ok || id != streams[1].ID {
		t.Errorf("ResolveStreamID(hex) = (%q, %v), want (%q, true)", id, ok, streams[1].ID)
	}

	// Unknown.
	if _, ok := qe.ResolveStreamID("nonexistent"); ok {
		t.Error("ResolveStreamID(unknown) returned ok=true")
	}
}

func TestQueryEngine_ResolveStreamID_CaseInsensitive(t *testing.T) {
	streams := aliasEngineStreams()
	qe := NewQueryEngine(streams, nil, aliasEngineTemplates())

	// Lowercase input must still resolve a mixed-case alias.
	id, ok := qe.ResolveStreamID("api-7b6.oomkilled")
	if !ok || id != streams[0].ID {
		t.Errorf("ResolveStreamID lowercase = (%q, %v), want (%q, true)", id, ok, streams[0].ID)
	}
	// Uppercase too.
	id, ok = qe.ResolveStreamID("API-7B6.OOMKILLED")
	if !ok || id != streams[0].ID {
		t.Errorf("ResolveStreamID upper = (%q, %v), want (%q, true)", id, ok, streams[0].ID)
	}
}

func TestQueryEngine_TemplateAliasPopulated(t *testing.T) {
	templates := aliasEngineTemplates()
	qe := NewQueryEngine(nil, nil, templates)
	for _, tpl := range templates {
		got := qe.AliasForTemplateID(tpl.ID)
		if got == "" {
			t.Errorf("AliasForTemplateID(%q) is empty", tpl.ID)
		}
	}
}

func TestQueryEngine_TemplateAliasCollisionSuffix(t *testing.T) {
	// Two templates whose patterns happen to alias to the same form
	// (different wildcards collapse). Collision suffix is 4 hex chars.
	a := &wirelogs.LogTemplate{
		ID: "ca00000000000000000000000000000000000000000000000000000000000001",
		// Same pattern twice with different IDs is unrealistic but the
		// alias is computed from pattern+severity so any cause of
		// collision is handled the same way.
		Pattern:  "Pod evicted",
		Severity: wirelogs.SeverityWarn,
		Alias:    "pod-evicted@warn",
	}
	b := &wirelogs.LogTemplate{
		ID:       "cb00000000000000000000000000000000000000000000000000000000000002",
		Pattern:  "Pod evicted",
		Severity: wirelogs.SeverityWarn,
		Alias:    "pod-evicted@warn",
	}
	qe := NewQueryEngine(nil, nil, []*wirelogs.LogTemplate{a, b})
	aliasA := qe.AliasForTemplateID(a.ID)
	aliasB := qe.AliasForTemplateID(b.ID)
	if aliasA == aliasB {
		t.Fatalf("template collision not resolved: both = %q", aliasA)
	}
	if aliasA != "pod-evicted@warn" {
		t.Errorf("first template alias = %q, want pod-evicted@warn", aliasA)
	}
	if !strings.HasPrefix(aliasB, "pod-evicted@warn-") {
		t.Errorf("second template alias = %q, want prefix pod-evicted@warn-", aliasB)
	}
	suffix := strings.TrimPrefix(aliasB, "pod-evicted@warn-")
	if suffix != b.ID[:4] {
		t.Errorf("template suffix = %q, want %q", suffix, b.ID[:4])
	}
}

func TestQueryEngine_ResolveTemplateID_Alias(t *testing.T) {
	templates := aliasEngineTemplates()
	qe := NewQueryEngine(nil, nil, templates)

	id, ok := qe.ResolveTemplateID("connection-refused@err")
	if !ok || id != templates[0].ID {
		t.Errorf("ResolveTemplateID(alias) = (%q, %v), want (%q, true)", id, ok, templates[0].ID)
	}
	id, ok = qe.ResolveTemplateID(templates[1].ID)
	if !ok || id != templates[1].ID {
		t.Errorf("ResolveTemplateID(hex) = (%q, %v), want (%q, true)", id, ok, templates[1].ID)
	}
	if _, ok := qe.ResolveTemplateID("nope"); ok {
		t.Error("ResolveTemplateID(unknown) returned ok=true")
	}
}

func TestQueryEngine_ResolveTemplateID_CaseInsensitive(t *testing.T) {
	templates := aliasEngineTemplates()
	qe := NewQueryEngine(nil, nil, templates)
	id, ok := qe.ResolveTemplateID("CONNECTION-REFUSED@ERR")
	if !ok || id != templates[0].ID {
		t.Errorf("ResolveTemplateID upper = (%q, %v), want (%q, true)", id, ok, templates[0].ID)
	}
}

func TestQueryEngine_RebuildFromLegacyStreams(t *testing.T) {
	// Legacy streams: no Alias field set. Engine must still derive one.
	legacy := &wirelogs.LogStream{
		ID: "1100000000000000000000000000000000000000000000000000000000000099",
		Labels: map[string]string{
			"pod_name": "legacy-pod",
			"reason":   "FailedScheduling",
		},
	}
	qe := NewQueryEngine([]*wirelogs.LogStream{legacy}, nil, nil)
	if alias := qe.AliasForStreamID(legacy.ID); alias != "legacy-pod.FailedScheduling" {
		t.Errorf("legacy stream alias = %q, want legacy-pod.FailedScheduling", alias)
	}
}

func TestQueryEngine_RebuildFromLegacyTemplates(t *testing.T) {
	legacy := &wirelogs.LogTemplate{
		ID:       "9900000000000000000000000000000000000000000000000000000000000099",
		Pattern:  "Pod was evicted",
		Severity: wirelogs.SeverityWarn,
	}
	qe := NewQueryEngine(nil, nil, []*wirelogs.LogTemplate{legacy})
	got := qe.AliasForTemplateID(legacy.ID)
	// `was` is NOT a stopword in the curated list, so it survives.
	if got != "pod-was-evicted@warn" {
		t.Errorf("legacy template alias = %q, want pod-was-evicted@warn", got)
	}
}

func TestQueryEngine_StreamAliasDeterministic(t *testing.T) {
	// Build the engine multiple times from the same input — the
	// collision-resolved aliases must be identical every run.
	streams := []*wirelogs.LogStream{
		{
			ID:     "ee00000000000000000000000000000000000000000000000000000000000001",
			Alias:  "dup.alias",
			Labels: map[string]string{"pod_name": "dup", "reason": "alias", "ns": "a"},
		},
		{
			ID:     "ee00000000000000000000000000000000000000000000000000000000000002",
			Alias:  "dup.alias",
			Labels: map[string]string{"pod_name": "dup", "reason": "alias", "ns": "b"},
		},
		{
			ID:     "ee00000000000000000000000000000000000000000000000000000000000003",
			Alias:  "dup.alias",
			Labels: map[string]string{"pod_name": "dup", "reason": "alias", "ns": "c"},
		},
	}
	first := NewQueryEngine(streams, nil, nil)
	for i := range 5 {
		again := NewQueryEngine(streams, nil, nil)
		for _, s := range streams {
			a := first.AliasForStreamID(s.ID)
			b := again.AliasForStreamID(s.ID)
			if a != b {
				t.Fatalf("non-deterministic: stream %q got %q vs %q on iter %d", s.ID, a, b, i)
			}
		}
	}
}
