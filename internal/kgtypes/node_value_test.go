// SPDX-License-Identifier: Apache-2.0

package kgtypes

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestValue_ScalarParity locks the kgtypes metadata-accessor free funcs to the
// scalar-map semantics of the store.Node wrapper methods they replace
// (cmd/knowledge-server/internal/store/node_value.go). Wire-decoded nodes carry
// nil hints, so the store
// wrapper resolves every key scalar and falls through to n.Metadata[key]; these
// free funcs ARE that fall-through, so the goldens are the exact store behavior
// for a hint-less node.
func TestValue_ScalarParity(t *testing.T) {
	t.Run("Value present", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"resource_type": "ec2:instance"}}
		if got := Value(n, "resource_type"); got != "ec2:instance" {
			t.Fatalf("Value present: got %q want %q", got, "ec2:instance")
		}
	})
	t.Run("Value absent returns empty", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"k": "v"}}
		if got := Value(n, "missing"); got != "" {
			t.Fatalf("Value absent: got %q want \"\"", got)
		}
	})
	t.Run("Value nil node returns empty", func(t *testing.T) {
		if got := Value(nil, "k"); got != "" {
			t.Fatalf("Value nil node: got %q want \"\"", got)
		}
	})
	t.Run("Value nil metadata returns empty", func(t *testing.T) {
		n := &knowledgev1.Node{}
		if got := Value(n, "k"); got != "" {
			t.Fatalf("Value nil metadata: got %q want \"\"", got)
		}
	})
	t.Run("Value present-but-empty returns empty", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"k": ""}}
		if got := Value(n, "k"); got != "" {
			t.Fatalf("Value present-empty: got %q want \"\"", got)
		}
	})
}

func TestValues_ScalarParity(t *testing.T) {
	t.Run("Values present is single-element slice", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"k": "v"}}
		got := Values(n, "k")
		if len(got) != 1 || got[0] != "v" {
			t.Fatalf("Values present: got %#v want [v]", got)
		}
	})
	t.Run("Values absent is nil", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"k": "v"}}
		if got := Values(n, "missing"); got != nil {
			t.Fatalf("Values absent: got %#v want nil", got)
		}
	})
	t.Run("Values nil node is nil", func(t *testing.T) {
		if got := Values(nil, "k"); got != nil {
			t.Fatalf("Values nil node: got %#v want nil", got)
		}
	})
	t.Run("Values nil metadata is nil", func(t *testing.T) {
		n := &knowledgev1.Node{}
		if got := Values(n, "k"); got != nil {
			t.Fatalf("Values nil metadata: got %#v want nil", got)
		}
	})
}

func TestSetValue_ScalarParity(t *testing.T) {
	t.Run("SetValue lazy-inits the map", func(t *testing.T) {
		n := &knowledgev1.Node{}
		SetValue(n, "k", "v")
		if n.Metadata == nil || n.Metadata["k"] != "v" {
			t.Fatalf("SetValue lazy-init: got %#v want map[k:v]", n.Metadata)
		}
	})
	t.Run("SetValue overwrites", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"k": "old"}}
		SetValue(n, "k", "new")
		if n.Metadata["k"] != "new" {
			t.Fatalf("SetValue overwrite: got %q want new", n.Metadata["k"])
		}
	})
	t.Run("SetValue nil node is no-op", func(t *testing.T) {
		SetValue(nil, "k", "v") // must not panic
	})
}

func TestDeleteValue_ScalarParity(t *testing.T) {
	t.Run("DeleteValue removes key", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"k": "v", "j": "w"}}
		DeleteValue(n, "k")
		if _, ok := n.Metadata["k"]; ok {
			t.Fatalf("DeleteValue: key k still present")
		}
		if n.Metadata["j"] != "w" {
			t.Fatalf("DeleteValue: collateral damage to j")
		}
	})
	t.Run("DeleteValue absent key is no-op", func(t *testing.T) {
		n := &knowledgev1.Node{Metadata: map[string]string{"k": "v"}}
		DeleteValue(n, "missing")
		if n.Metadata["k"] != "v" {
			t.Fatalf("DeleteValue absent: mutated map")
		}
	})
	t.Run("DeleteValue nil node / nil metadata is no-op", func(t *testing.T) {
		DeleteValue(nil, "k")                 // must not panic
		DeleteValue(&knowledgev1.Node{}, "k") // must not panic
	})
}

func TestMetaSetMeta_AliasParity(t *testing.T) {
	n := &knowledgev1.Node{}
	SetMeta(n, "k", "v")
	if Meta(n, "k") != "v" {
		t.Fatalf("Meta/SetMeta alias: got %q want v", Meta(n, "k"))
	}
	if Value(n, "k") != "v" {
		t.Fatalf("SetMeta must write the same map Value reads: got %q", Value(n, "k"))
	}
}
