// SPDX-License-Identifier: Apache-2.0

package kgtypes

import (
	"testing"
)

// TestHiveEdgeTypes_WireLiterals pins the client kgtypes hive edge-type literals
// to their agreed cross-module wire strings. The server store vocabulary
// (cmd/knowledge-server/internal/store/edge_types_vocab.go) carries independent
// copies of these consts — a deliberate per-module duplicate (no shared
// package). This drift-guard plus its server twin (TestHiveEdgeTypes_WireLiterals
// in store) fail if either module's literal changes without the other.
//
// These are NEW edges, NOT reuse of EdgeKGContains ("contains", parent→child):
// EdgeContainedBy is the OPPOSITE direction (child→parent), and EdgeRespondsTo
// is the ack-reply edge (result message → original message).
func TestHiveEdgeTypes_WireLiterals(t *testing.T) {
	cases := []struct {
		got  EdgeType
		want string
	}{
		{EdgeContainedBy, "contained-by"},
		{EdgeRespondsTo, "responds-to"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Fatalf("hive edge-type literal = %q, want %q (must match the server store const)", c.got, c.want)
		}
	}
}
