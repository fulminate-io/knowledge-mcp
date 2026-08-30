// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFakeEmbedder_DeterministicWidthExact pins the three properties the
// end-to-end venue depends on: an EXACT byte length, determinism across
// independent constructions, and distinct bytes for distinct texts.
//
// The length assertion is a CONCRETE 32, not Dimension/8. Comparing
// against Dimension/8 is satisfied by a degenerate width — 0 bytes equals
// 0/8 — which is precisely the silent-empty-vector failure this arm could
// otherwise ship. The constant is the external expectation.
func TestFakeEmbedder_DeterministicWidthExact(t *testing.T) {
	ctx := context.Background()
	cfg := func() *Config {
		return &Config{Provider: ProviderFake, Dimension: 256, Dtype: "ubinary"}
	}

	a, err := NewEmbedder(ctx, cfg())
	require.NoError(t, err)
	b, err := NewEmbedder(ctx, cfg())
	require.NoError(t, err)

	vec, err := a.EmbedBinary(ctx, "package main")
	require.NoError(t, err)
	require.Len(t, vec, 32, "256 bits at one bit per dimension is exactly 32 bytes")
	assert.NotEqual(t, make([]byte, 32), vec, "an all-zero vector would satisfy the length check while carrying nothing")

	// Two INDEPENDENT constructions must agree — the cross-process
	// determinism the venue asserts against, exercised the only way one
	// process can.
	again, err := b.EmbedBinary(ctx, "package main")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(vec, again), "two independent constructions must produce identical bytes")

	// Distinct texts must produce distinct vectors, or the venue cannot
	// tell one indexed document from another.
	other, err := a.EmbedBinary(ctx, "package other")
	require.NoError(t, err)
	require.Len(t, other, 32)
	assert.False(t, bytes.Equal(vec, other), "distinct texts must produce distinct vectors")

	// The batch method agrees with the single method, item for item, and
	// preserves order.
	batch, err := a.EmbedBinaryBatch(ctx, []string{"package main", "package other"})
	require.NoError(t, err)
	require.Len(t, batch, 2)
	assert.True(t, bytes.Equal(batch[0], vec))
	assert.True(t, bytes.Equal(batch[1], other))
	for i, v := range batch {
		assert.Len(t, v, 32, "batch item %d must be exactly 32 bytes", i)
	}

	assert.True(t, a.Available(), "the fake needs no credential and is always available")
}

// TestFakeEmbedder_WidthAboveDigest exercises the counter-extension path
// directly, since the accepted dimension keeps production at 32 bytes. It
// builds the arm below the registry on purpose — Validate would refuse the
// wider config, and the derivation still has to be total and deterministic
// at any width the dtype gate ever admits.
func TestFakeEmbedder_WidthAboveDigest(t *testing.T) {
	wide := &fakeEmbedder{width: 96}
	a := wide.vector("package main")
	b := wide.vector("package main")
	require.Len(t, a, 96, "the extension must produce EXACTLY the requested width")
	assert.True(t, bytes.Equal(a, b), "the extension must stay deterministic")
	assert.False(t, bytes.Equal(a[:32], a[32:64]), "extension blocks must differ, not repeat the seed digest")
	assert.False(t, bytes.Equal(wide.vector("package other"), a))
}

// TestFakeEmbedder_RegisteredUnderConfigVocabulary asserts the arm is
// reachable by the value an operator actually writes in [embedder].
func TestFakeEmbedder_RegisteredUnderConfigVocabulary(t *testing.T) {
	require.True(t, HasProvider(ProviderFake), "the fake must self-register from init()")
	assert.Equal(t, "fake", string(ProviderFake), "registration must use the config vocabulary value")

	e, err := NewEmbedder(context.Background(), &Config{Provider: Provider("fake"), Dimension: 256, Dtype: "ubinary"})
	require.NoError(t, err, "the literal config value must resolve to the arm")
	_, ok := e.(*fakeEmbedder)
	assert.True(t, ok, "provider \"fake\" resolved to %T", e)
}
