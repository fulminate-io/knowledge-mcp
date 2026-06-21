// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestInterpret_Emit_SkipsRowWithNoIdentity pins the no-identity-signal skip: a
// row resolving name="" with no identity field is dropped (SkippedChunks++, one
// fewer node) rather than emitting a hex-ID placeholder off row.NodeID.
func TestInterpret_Emit_SkipsRowWithNoIdentity(t *testing.T) {
	named := &knowledgev1.Node{Id: "s1", Type: "section", SymbolName: "Message Router"}
	empty := &knowledgev1.Node{Id: "s2", Type: "section", SymbolName: ""}
	sv := &sourceView{
		byID:   map[string]*knowledgev1.Node{"s1": named, "s2": empty},
		byType: map[string][]*knowledgev1.Node{"section": {named, empty}},
	}
	body := `select section
emit pattern {
    name := section.symbol_name
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 1, "the empty-name row was skipped, only the named section emitted")
	assert.Equal(t, "Message Router", result.Nodes[0].SymbolName)
	assert.Equal(t, 1, result.Stats.SkippedChunks, "the no-identity row counts as a skipped chunk")
}

// TestInterpret_Emit_ExplicitIdentityDrivesStableID pins emitIdentity's
// precedence: the explicit `identity` field feeds StableID over `name`, the
// identity field is NOT persisted on the node, and the node name remains the
// symbol_name.
func TestInterpret_Emit_ExplicitIdentityDrivesStableID(t *testing.T) {
	sv := oneSectionView() // single "Message Router" section
	target := recipeTargetSpec()
	const slug = "eip"
	body := `select section
emit pattern {
    name := section.symbol_name
    identity := concat("fixed-", section.symbol_name)
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, target, slug, Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 1)
	n := result.Nodes[0]
	// Id is computed from the identity field value, not the name.
	wantID := StableID(TargetKey(target), slug, "pattern", "fixed-Message Router")
	assert.Equal(t, wantID, n.Id, "explicit identity drives StableID")
	assert.NotEqual(t, StableID(TargetKey(target), slug, "pattern", "Message Router"), n.Id,
		"the id is NOT derived from the name when identity is set")
	// The identity field is consumed by StableID only — never stored.
	assert.NotContains(t, n.Metadata, "identity", "identity must not land in Metadata")
	assert.Equal(t, "Message Router", n.SymbolName, "the node name is still the symbol_name")
}

// TestInterpret_Emit_FieldFolding pins assembleEmittedNode's field routing: each
// recognized key maps to its named Node field, only unrecognized keys land in
// Metadata, the `type` field overrides the emit NodeType, and symbol_name
// aliases name.
func TestInterpret_Emit_FieldFolding(t *testing.T) {
	sv := oneSectionView()
	body := `select section
emit pattern {
    type := "use_case"
    name := section.symbol_name
    summary := "s"
    description := "d"
    content := "c"
    status := "active"
    custom_a := "va"
    custom_b := "vb"
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 1)
	n := result.Nodes[0]
	assert.Equal(t, "use_case", n.Type, "the 'type' field overrides the rule NodeType")
	assert.Equal(t, "Message Router", n.SymbolName)
	assert.Equal(t, "s", n.Summary)
	assert.Equal(t, "d", n.Description)
	assert.Equal(t, "c", n.Content)
	assert.Equal(t, "active", n.Status)
	// Only the two unrecognized keys land in Metadata — nothing else.
	assert.Equal(t, map[string]string{"custom_a": "va", "custom_b": "vb"}, n.Metadata)

	t.Run("SymbolNameAlias", func(t *testing.T) {
		// Observed behavior: the no-identity skip guard (evalEmit) keys on the
		// LITERAL "name" / "identity" field keys, NOT the symbol_name alias — so a
		// row carrying only symbol_name would be skipped before assembleEmittedNode
		// runs. Supply an explicit identity so the row survives the guard; that
		// isolates the alias path (symbol_name → Node.SymbolName) under test.
		aliasBody := `select section
emit pattern {
    symbol_name := "X"
    identity := "alias-id"
}`
		aliasRecipe := parseOrFatal(t, aliasBody)
		res, err := Interpret(context.Background(), aliasRecipe, sv, recipeTargetSpec(), "eip", Options{})
		require.NoError(t, err)
		require.Len(t, res.Nodes, 1)
		assert.Equal(t, "X", res.Nodes[0].SymbolName, "symbol_name aliases the name field")
		assert.NotContains(t, res.Nodes[0].Metadata, "symbol_name", "the alias does not leak into Metadata")
	})
}

// TestInterpret_Emit_SourceDefaultAndExplicit pins assembleEmittedNode's Source
// defaulting: with no source field the Source is "recipe:<slug>"; an explicit
// source field overrides it.
func TestInterpret_Emit_SourceDefaultAndExplicit(t *testing.T) {
	sv := oneSectionView()
	const slug = "eip"

	t.Run("DefaultRecipeTag", func(t *testing.T) {
		body := `select section
emit pattern {
    name := section.symbol_name
}`
		recipe := parseOrFatal(t, body)
		result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), slug, Options{})
		require.NoError(t, err)
		require.Len(t, result.Nodes, 1)
		assert.Equal(t, "recipe:"+slug, result.Nodes[0].Source)
	})

	t.Run("ExplicitSource", func(t *testing.T) {
		body := `select section
emit pattern {
    name := section.symbol_name
    source := "manual"
}`
		recipe := parseOrFatal(t, body)
		result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), slug, Options{})
		require.NoError(t, err)
		require.Len(t, result.Nodes, 1)
		assert.Equal(t, "manual", result.Nodes[0].Source, "an explicit source field overrides the default tag")
	})
}
