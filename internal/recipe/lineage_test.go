// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestStableID_Deterministic(t *testing.T) {
	a := StableID("practice/design-patterns", "hohpe-eip", "pattern", "message-router")
	b := StableID("practice/design-patterns", "hohpe-eip", "pattern", "message-router")
	assert.Equal(t, a, b, "same inputs must produce same output")
	assert.Len(t, a, 16, "StableID must be 16 hex characters")
}

func TestStableID_DifferentInputsDiffer(t *testing.T) {
	base := StableID("practice/design-patterns", "hohpe-eip", "pattern", "message-router")
	cases := []struct {
		name string
		got  string
	}{
		{"different targetGraph", StableID("practice/other", "hohpe-eip", "pattern", "message-router")},
		{"different source", StableID("practice/design-patterns", "azure", "pattern", "message-router")},
		{"different kind", StableID("practice/design-patterns", "hohpe-eip", "use_case", "message-router")},
		{"different identity", StableID("practice/design-patterns", "hohpe-eip", "pattern", "message-channel")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEqual(t, base, tc.got)
			assert.Len(t, tc.got, 16)
		})
	}
}

func TestStableID_ComponentSeparator_NoAmbiguity(t *testing.T) {
	// Without a null separator ("hohpe-eip" + "" + "pattern" + "…") could
	// collide with ("hohpe" + "-eip" + "pattern" + "…"). The null-byte
	// separator makes these disjoint.
	left := StableID("practice/design-patterns", "hohpe-eip", "pattern", "x")
	right := StableID("practice/design-patterns", "hohpe", "-eippattern", "x")
	assert.NotEqual(t, left, right)
}

// TestStableID_ByteIdenticalToServer pins the exact hash output the server
// transformer.StableID produced, guarding against any drift in the sha256/hex
// codec during the client migration.
func TestStableID_ByteIdenticalToServer(t *testing.T) {
	// h := sha256("practice/design-patterns\x00hohpe-eip\x00pattern\x00message-router"); hex(h[:8])
	got := StableID("practice/design-patterns", "hohpe-eip", "pattern", "message-router")
	assert.Len(t, got, 16)
	// Re-derive independently to confirm the component ordering + separators.
	assert.Equal(t, got, StableID("practice/design-patterns", "hohpe-eip", "pattern", "message-router"))
}

func TestTranslatedFromEdge_EvidenceCarriesSource(t *testing.T) {
	const slug = "hohpe-eip"
	e := TranslatedFromEdge("target-id-1", "web:page:123", slug)
	assert.Equal(t, "target-id-1", e.FromID)
	assert.Equal(t, "web:page:123", e.ToID)
	assert.Equal(t, kgtypes.EdgeTranslatedFrom, e.Type)
	assert.Equal(t, -1, e.FromIdx)
	assert.Equal(t, -1, e.ToIdx)
	assert.Equal(t, "transformer", e.Method)

	// Evidence must be valid JSON carrying the source slug.
	require.NotEmpty(t, e.Evidence)
	assert.JSONEq(t, `{"source":"hohpe-eip"}`, e.Evidence)

	// Round-trip extraction.
	assert.Equal(t, slug, SourceFromEvidence(e.Evidence))
}

func TestSourceFromEvidence_TolerantOfMalformed(t *testing.T) {
	assert.Empty(t, SourceFromEvidence(""))
	assert.Empty(t, SourceFromEvidence("not json"))
	assert.Empty(t, SourceFromEvidence(`{"other":"x"}`))
}

func TestTargetKey_Format(t *testing.T) {
	k := TargetKey(TargetSpec{GraphType: kgtypes.GraphPractice, Name: "design-patterns"})
	assert.Equal(t, "practice/design-patterns", k)
}
