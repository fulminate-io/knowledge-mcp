// SPDX-License-Identifier: Apache-2.0

// Package tools — alias-resolution tests for traverseLogs.
//
// These verify the dispatcher accepts an alias anywhere a hex stream or
// template ID is accepted: stream alias, template alias, and the legacy
// hex-passthrough form. Unresolvable input must return a clear error
// listing all three accepted forms.
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineStreamAlias resolves the alias for the given stream ID via an engine
// rebuilt from the fixture corpus — the engine is the source of truth for
// collision-suffixed aliases, and the process-local logs.LookupEngine registry
// is not populated under the fake.
func engineStreamAlias(t *testing.T, f *logGraphFixture, streamID string) string {
	t.Helper()
	alias := engineFromCorpus(f.Nodes).AliasForStreamID(streamID)
	require.NotEmpty(t, alias, "stream %q has no alias", streamID)
	return alias
}

func engineTemplateAlias(t *testing.T, f *logGraphFixture, templateID string) string {
	t.Helper()
	alias := engineFromCorpus(f.Nodes).AliasForTemplateID(templateID)
	require.NotEmpty(t, alias, "template %q has no alias", templateID)
	return alias
}

func TestTraverseLogs_ResolvesStreamAlias(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)
	require.NotEmpty(t, f.StreamID)
	alias := engineStreamAlias(t, f, f.StreamID)

	byHex := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: f.StreamID, Direction: "both",
	})
	require.False(t, byHex.IsError, "hex traverse: %s", resultText(byHex))

	byAlias := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: alias, Direction: "both",
	})
	require.False(t, byAlias.IsError, "alias traverse: %s", resultText(byAlias))

	// Both invocations should produce the same body — alias resolution
	// is a transparent rewrite, not a different code path.
	assert.Equal(t, resultText(byHex), resultText(byAlias),
		"alias and hex must produce identical traverse output")
}

func TestTraverseLogs_ResolvesTemplateAlias(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)
	require.NotEmpty(t, f.TemplateID)
	alias := engineTemplateAlias(t, f, f.TemplateID)

	byHex := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: f.TemplateID, Direction: "down",
	})
	require.False(t, byHex.IsError, "hex traverse: %s", resultText(byHex))

	byAlias := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: alias, Direction: "down",
	})
	require.False(t, byAlias.IsError, "alias traverse: %s", resultText(byAlias))
	assert.Equal(t, resultText(byHex), resultText(byAlias),
		"alias and hex must produce identical traverse output")
}

func TestTraverseLogs_HexPassthrough(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)
	require.NotEmpty(t, f.TemplateID)

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: f.TemplateID, Direction: "down",
	})
	require.False(t, result.IsError, "expected success: %s", resultText(result))
	assert.Contains(t, resultText(result), "Log template",
		"hex form must still resolve to the template detail header")
}

func TestTraverseLogs_UnresolvableInput(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: "totally-bogus", Direction: "down",
	})
	require.True(t, result.IsError, "expected error for unresolvable start")
	msg := resultText(result)
	// The error must mention all three input forms so an agent knows
	// where to look next.
	for _, want := range []string{"stream alias", "template alias", "hex"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}
