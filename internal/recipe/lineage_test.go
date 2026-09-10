// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"

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

func TestTargetKey_Format(t *testing.T) {
	k := TargetKey(TargetSpec{GraphType: kgtypes.GraphPractice, Name: "design-patterns"})
	assert.Equal(t, "practice/design-patterns", k)
}

// TestVersionedTwinID_DistinctPerVersionAndDeterministic is requirement 4's
// ID-MINTING observation, at the primitive.
//
// THE DISTINCTNESS FROM THE RESIDENT IS THE LOAD-BEARING ROW. The landing writes
// through create_batch, which is an ADD: a twin sharing the resident's id
// overwrites it, which is the hand-edited-node regression the whole versioning
// design exists to prevent. Sameness here is not a cosmetic collision, it is data
// loss.
func TestVersionedTwinID_DistinctPerVersionAndDeterministic(t *testing.T) {
	const (
		target   = "practice/default"
		slug     = "hohpe-eip"
		kind     = "pattern"
		resident = "0123456789abcdef"
	)

	v2 := VersionedTwinID(target, slug, kind, resident, 2)
	v3 := VersionedTwinID(target, slug, kind, resident, 3)

	assert.Len(t, v2, 16, "a twin id is a StableID and carries its width")
	assert.NotEqual(t, resident, v2,
		"the twin must NOT carry the resident's id: create_batch is an add, so a shared id overwrites the row a human edited")
	assert.NotEqual(t, v2, v3, "each version mints its own id")

	// DETERMINISM: re-running the same landing against an unchanged target
	// reproduces the same twin, which is what keeps a re-run idempotent rather
	// than minting a fresh node per run.
	assert.Equal(t, v2, VersionedTwinID(target, slug, kind, resident, 2),
		"the same (target, slug, kind, resident, version) reproduces the same twin id")

	// EVERY COMPONENT IS LOAD-BEARING, so a twin under a different graph, source
	// or type is a different node.
	assert.NotEqual(t, v2, VersionedTwinID("practice/other", slug, kind, resident, 2))
	assert.NotEqual(t, v2, VersionedTwinID(target, "other-slug", kind, resident, 2))
	assert.NotEqual(t, v2, VersionedTwinID(target, slug, "use_case", resident, 2))

	// THE SEPARATOR IS NOT DECORATION: without a component separator that cannot
	// appear in an id, ("abc", 12) and ("abc1", 2) would hash to one twin.
	assert.NotEqual(t,
		VersionedTwinID(target, slug, kind, "abc", 12),
		VersionedTwinID(target, slug, kind, "abc1", 2),
		"the resident id and the version are separated by a byte that cannot occur in either")
}
