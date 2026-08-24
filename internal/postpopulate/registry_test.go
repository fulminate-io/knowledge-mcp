// SPDX-License-Identifier: Apache-2.0

package postpopulate_test

import (
	"context"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// TestRegister_DeclaresBreadth pins the declared-value round trip: whatever
// breadth a collector states at registration is what Lookup hands the
// orchestrator back. Catches a Lookup that drops or defaults the declaration,
// which would silently take the zero-value arm of the orchestrator's switch.
func TestRegister_DeclaresBreadth(t *testing.T) {
	stub := func(context.Context, postpopulate.GraphCaller, string) error { return nil }

	postpopulate.Register("registrytest-scoped", postpopulate.BreadthScoped, stub)
	scoped, ok := postpopulate.Lookup("registrytest-scoped")
	if !ok {
		t.Fatal("Lookup(registrytest-scoped): not registered")
	}
	if scoped.Breadth != postpopulate.BreadthScoped {
		t.Errorf("scoped hook breadth = %q, want %q", scoped.Breadth, postpopulate.BreadthScoped)
	}
	if scoped.Fn == nil {
		t.Error("scoped hook Fn is nil, want the registered stub")
	}

	postpopulate.Register("registrytest-broad", postpopulate.BreadthFamilyBroad, stub)
	broad, ok := postpopulate.Lookup("registrytest-broad")
	if !ok {
		t.Fatal("Lookup(registrytest-broad): not registered")
	}
	if broad.Breadth != postpopulate.BreadthFamilyBroad {
		t.Errorf("broad hook breadth = %q, want %q", broad.Breadth, postpopulate.BreadthFamilyBroad)
	}
	if broad.Fn == nil {
		t.Error("broad hook Fn is nil, want the registered stub")
	}
}

// TestRegister_PanicsOnUnknownBreadth pins the third panic. It is the
// structural enforcement: a breadth outside the two locked constants can never
// reach the orchestrator, because registration fails loudly at init. "broad" is
// a plausible near-miss of the locked "family-broad" spelling.
func TestRegister_PanicsOnUnknownBreadth(t *testing.T) {
	stub := func(context.Context, postpopulate.GraphCaller, string) error { return nil }

	for _, breadth := range []postpopulate.Breadth{"", "broad"} {
		t.Run(string(breadth), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("Register with breadth %q did not panic", breadth)
				}
			}()
			postpopulate.Register("registrytest-unknown", breadth, stub)
		})
	}
}
