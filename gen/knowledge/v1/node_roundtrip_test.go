// SPDX-License-Identifier: Apache-2.0

// Package knowledgev1_test holds hand-written tests for the generated
// knowledge.v1 proto bindings. It lives in the external _test package so it
// is never overwritten by `buf generate` (which only emits *.pb.go /
// *.connect.go) and so it cannot accidentally reach into the generated
// package internals. These tests are PURE proto-message round-trips: they
// import only the generated bindings + google.golang.org/protobuf/proto +
// testify, and deliberately do NOT import the server store package
// (cmd/knowledge-server/internal/store) — the value-embed Node
// message must stand on its own on the wire.
package knowledgev1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestNode_RoundTrip_AllFields sets every one of the 21 persistent
// store.Node fields (mirrored 1:1 by knowledgev1.Node, field numbers 1-21 per
// node_encoding.go:42-65) to a distinct non-zero value, marshals it to the
// wire, unmarshals into a fresh message, and asserts both proto.Equal and a
// per-field spot-check. The per-field asserts mean a field added to the
// message later without a matching assert is visible as an UNCHECKED field
// rather than silently passing on proto.Equal alone.
func TestNode_RoundTrip_AllFields(t *testing.T) {
	orig := &knowledgev1.Node{
		Id:           "node-abc123",
		Type:         "function_declaration",
		SymbolName:   "DoTheThing",
		FilePath:     "pkg/example/thing.go",
		Language:     "go",
		StartLine:    12,
		EndLine:      48,
		Content:      "func DoTheThing() error { return nil }",
		Signature:    "func DoTheThing() error",
		Summary:      "Does the thing and returns an error.",
		Description:  "A human-readable description of the node.",
		Source:       "collector:git",
		Status:       "active",
		Metadata:     map[string]string{"choice": "option-a", "rationale": "because"},
		CreatedAt:    1_700_000_000_000_000_001,
		UpdatedAt:    1_700_000_000_000_000_002,
		Keywords:     "thing do error return func",
		IsExported:   true,
		TombstonedAt: 1_700_000_000_000_000_003,
		IsTest:       true,
		TestKind:     "helper",
	}

	wire, err := proto.Marshal(orig)
	require.NoError(t, err, "proto.Marshal must succeed")

	got := &knowledgev1.Node{}
	require.NoError(t, proto.Unmarshal(wire, got), "proto.Unmarshal must succeed")

	require.True(t, proto.Equal(orig, got), "round-tripped Node must be proto.Equal to the original")

	// Per-field spot-asserts — one per persistent store.Node field (21 total).
	assert.Equal(t, "node-abc123", got.GetId())                                                         // 1  store.Node.ID
	assert.Equal(t, "function_declaration", got.GetType())                                              // 2  store.Node.Type
	assert.Equal(t, "DoTheThing", got.GetSymbolName())                                                  // 3  store.Node.SymbolName
	assert.Equal(t, "pkg/example/thing.go", got.GetFilePath())                                          // 4  store.Node.FilePath
	assert.Equal(t, "go", got.GetLanguage())                                                            // 5  store.Node.Language
	assert.Equal(t, int32(12), got.GetStartLine())                                                      // 6  store.Node.StartLine
	assert.Equal(t, int32(48), got.GetEndLine())                                                        // 7  store.Node.EndLine
	assert.Equal(t, "func DoTheThing() error { return nil }", got.GetContent())                         // 8  store.Node.Content
	assert.Equal(t, "func DoTheThing() error", got.GetSignature())                                      // 9  store.Node.Signature
	assert.Equal(t, "Does the thing and returns an error.", got.GetSummary())                           // 10 store.Node.Summary
	assert.Equal(t, "A human-readable description of the node.", got.GetDescription())                  // 11 store.Node.Description
	assert.Equal(t, "collector:git", got.GetSource())                                                   // 12 store.Node.Source
	assert.Equal(t, "active", got.GetStatus())                                                          // 13 store.Node.Status
	assert.Equal(t, map[string]string{"choice": "option-a", "rationale": "because"}, got.GetMetadata()) // 14 store.Node.Metadata
	assert.Equal(t, int64(1_700_000_000_000_000_001), got.GetCreatedAt())                               // 15 store.Node.CreatedAt
	assert.Equal(t, int64(1_700_000_000_000_000_002), got.GetUpdatedAt())                               // 16 store.Node.UpdatedAt
	assert.Equal(t, "thing do error return func", got.GetKeywords())                                    // 17 store.Node.Keywords
	assert.True(t, got.GetIsExported())                                                                 // 18 store.Node.IsExported
	assert.Equal(t, int64(1_700_000_000_000_000_003), got.GetTombstonedAt())                            // 19 store.Node.TombstonedAt
	assert.True(t, got.GetIsTest())                                                                     // 20 store.Node.IsTest
	assert.Equal(t, "helper", got.GetTestKind())                                                        // 21 store.Node.TestKind
}

// TestEdge_FieldComplete sets all 8 fields of knowledgev1.Edge to non-zero
// values, round-trips through the wire, and asserts proto.Equal + per-field
// equality. This mechanically proves the Edge message carries every persistent
// store.Edge field (cmd/knowledge-server/internal/store/graph_types.go:327,
// where store.Edge is now a type alias of this very message):
//
//	1 FromID        2 ToID          3 Type          4 Weight
//	5 Confidence    6 Method        7 Evidence      8 LastValidated
//
// — the in-code form of the Phase 1.2 completeness confirmation. A store.Edge
// field added later without a matching proto field would leave one of these
// asserts uncovered, surfacing the gap.
func TestEdge_FieldComplete(t *testing.T) {
	orig := &knowledgev1.Edge{
		FromId:        "node-from",                      // 1 store.Edge.FromID
		ToId:          "node-to",                        // 2 store.Edge.ToID
		Type:          "depends-on",                     // 3 store.Edge.Type
		Weight:        3.5,                              // 4 store.Edge.Weight
		Confidence:    0.87,                             // 5 store.Edge.Confidence
		Method:        "tier1:dockerfile-parse",         // 6 store.Edge.Method
		Evidence:      "FROM golang:1.23 in Dockerfile", // 7 store.Edge.Evidence
		LastValidated: 1_700_000_000_000_000_009,        // 8 store.Edge.LastValidated (unix nanos)
	}

	wire, err := proto.Marshal(orig)
	require.NoError(t, err, "proto.Marshal must succeed")

	got := &knowledgev1.Edge{}
	require.NoError(t, proto.Unmarshal(wire, got), "proto.Unmarshal must succeed")

	require.True(t, proto.Equal(orig, got), "round-tripped Edge must be proto.Equal to the original")

	assert.Equal(t, "node-from", got.GetFromId())
	assert.Equal(t, "node-to", got.GetToId())
	assert.Equal(t, "depends-on", got.GetType())
	assert.InEpsilon(t, 3.5, got.GetWeight(), 1e-9)
	assert.InEpsilon(t, 0.87, got.GetConfidence(), 1e-9)
	assert.Equal(t, "tier1:dockerfile-parse", got.GetMethod())
	assert.Equal(t, "FROM golang:1.23 in Dockerfile", got.GetEvidence())
	assert.Equal(t, int64(1_700_000_000_000_000_009), got.GetLastValidated())
}
