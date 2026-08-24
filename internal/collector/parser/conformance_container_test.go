// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// TestDeclaredConformanceInsideBracedContainers pins that a conformance
// declared INSIDE a braced namespace resolves and emits at both levels.
//
// THE DEFECT THIS CLOSES was a silent total loss, not a partial one. A braced
// `namespace X { ... }` is a CONTAINER here, so the declarations inside it take
// it as their parent — while the supertype lookup asked with an EMPTY parent.
// Nothing matched, every such supertype counted as unresolvable, and no
// conformance declared inside a braced namespace produced an edge of any kind.
// Measured before the fix on the php fixture below: unresolvable=1, and zero
// pairs.
//
// EACH LANGUAGE HERE WAS MEASURED RATHER THAN ASSUMED TO SHARE THE SHAPE. php
// and C# index identically (the class and the interface both take the namespace
// as parent). TypeScript's `namespace X {}` does NOT — it is not admitted as a
// container, so its declarations stay top-level — and it is included as the
// control that shows this fix is about containers rather than about namespaces.
func TestDeclaredConformanceInsideBracedContainers(t *testing.T) {
	t.Run("php_braced_namespace", func(t *testing.T) {
		files := []fixtureFile{{path: "app/braced.php", src: `<?php
namespace App {
    interface Writer {
        public function write();
    }

    class Server implements Writer {
        public function write() {}
    }
}
`}}
		// ROUTED THROUGH THE REAL PHP ARM rather than injected facts: the arm
		// captures this shape itself, so letting it do so covers one more link
		// of the chain — capture, containment, scoping and pairing — for free.
		ix := ownerFixtureIndex(t, files)
		pairs, stats := deriveDeclaredConformance(ix)
		require.Zero(t, stats.Unresolvable,
			"the supertype is declared beside the subtype inside the same braced namespace, so it "+
				"must resolve — an empty-parent lookup used to miss it entirely")
		require.Len(t, pairs, 1, "the type-level relationship is derived")
		require.Equal(t, "app/braced.php:App.Writer", pairs[0].supertype.NodeID)
		require.Equal(t, "app/braced.php:App.Server", pairs[0].subtype.NodeID)
		require.True(t,
			ownerMemberPaired(pairs, "app/braced.php:Writer.write", "app/braced.php:Server.write"),
			"and the MEMBER level pairs too, which is the half that had nothing to start from "+
				"while the type level was resolving to nothing")
	})

	t.Run("csharp_braced_namespace", func(t *testing.T) {
		// MEASURED TO SHARE THE SHAPE rather than assumed to: C# indexes a braced
		// namespace's declarations under it exactly as php does.
		files := []fixtureFile{{path: "app/Braced.cs", src: `namespace App {
    interface IWriter {
        void Write();
    }

    class Server : IWriter {
        public void Write() {}
    }
}
`}}
		ix := ownerFixtureIndexInjected(t, files, func(chunk treesitter.Chunk, nodeID string) *treesitter.TypeFacts {
			switch nodeID {
			case "app/Braced.cs:App.IWriter":
				return &treesitter.TypeFacts{IsInterface: true}
			case "app/Braced.cs:App.Server":
				return &treesitter.TypeFacts{Conforms: []treesitter.DeclaredSupertype{
					{Text: "IWriter", Kind: treesitter.ConformUndeclared},
				}}
			}
			return nil
		})
		pairs, stats := deriveDeclaredConformance(ix)
		require.Zero(t, stats.Unresolvable, "the braced namespace's own interface resolves")
		require.Len(t, pairs, 1)
		require.Equal(t, "app/Braced.cs:App.Server", pairs[0].subtype.NodeID)
		require.True(t,
			ownerMemberPaired(pairs, "app/Braced.cs:IWriter.Write", "app/Braced.cs:Server.Write"),
			"and its member pairs")
	})

	t.Run("typescript_namespace_was_never_affected", func(t *testing.T) {
		// THE CONTROL THAT NAMES THE BOUNDARY. TypeScript's `namespace X {}`
		// parses to a kind this collector does not admit as a container, so its
		// declarations are top-level and the empty-parent lookup always found
		// them. Measured, not assumed — and it is what shows the repair is about
		// CONTAINER PARENTING rather than about the word "namespace".
		files := []fixtureFile{{path: "web/ns.ts", src: `namespace App {
  export interface Writer { write(): void }
  export class Server implements Writer { write(): void {} }
}
`}}
		ix := ownerFixtureIndexInjected(t, files, func(chunk treesitter.Chunk, nodeID string) *treesitter.TypeFacts {
			switch nodeID {
			case "web/ns.ts:Writer":
				return &treesitter.TypeFacts{IsInterface: true}
			case "web/ns.ts:Server":
				return &treesitter.TypeFacts{Conforms: []treesitter.DeclaredSupertype{
					{Text: "Writer", Kind: treesitter.ConformImplements},
				}}
			}
			return nil
		})
		require.Contains(t, ix.byID, "web/ns.ts:Server",
			"control: a TypeScript namespace's class is indexed WITHOUT a container parent")
		require.Empty(t, ix.byID["web/ns.ts:Server"].Parent,
			"which is exactly why this language never lost its supertype lookup")

		pairs, stats := deriveDeclaredConformance(ix)
		require.Zero(t, stats.Unresolvable)
		require.Len(t, pairs, 1, "and it resolves, as it did before this change")
	})

	t.Run("php_braced_namespace_ownership_still_holds", func(t *testing.T) {
		// THE CROSSING CASE INSIDE THE NEWLY-RESOLVING SHAPE, and it is the one
		// that proves the two repairs compose rather than one undoing the other.
		// Two classes named Server inside ONE braced namespace share
		// {Scope, Parent, Name}; only the second declares the conformance, and
		// only the FIRST declares `write`. Resolving the supertype now reaches
		// the member stage — which is precisely where the ownership check has to
		// still hold, or widening the lookup would have re-opened the wrong-edge
		// path on a shape that previously emitted nothing at all.
		files := []fixtureFile{{path: "app/two.php", src: `<?php
namespace App {
    interface Writer {
        public function write();
        public function flush();
    }

    class Server {
        public function write() {}
    }

    class Server implements Writer {
        public function flush() {}
    }
}
`}}
		// ALSO THROUGH THE REAL ARM: the php arm captures the implements clause
		// on the second class itself, so the conforming container is whichever
		// record the arm gave a clause to rather than one this test selected.
		ix := ownerFixtureIndex(t, files)
		var conforming []string
		for id, rec := range ix.byID {
			if len(rec.Conforms) > 0 {
				conforming = append(conforming, id)
			}
		}
		// ASSERTED BY COUNT, not by non-emptiness. A range over a map takes
		// whichever record it reaches last, so a fixture that grew a second
		// clause would silently pick one and the case would read as passing
		// against a subject nobody chose.
		require.Len(t, conforming, 1, "control: the arm captured a clause on exactly one class")
		conformingID := conforming[0]

		pairs, stats := deriveDeclaredConformance(ix)
		require.Zero(t, stats.Unresolvable, "the supertype resolves inside the braced namespace")
		require.Len(t, pairs, 1)
		require.Equal(t, conformingID, pairs[0].subtype.NodeID,
			"control: the SECOND Server is the subtype")

		require.False(t, ownerAnySpecPaired(pairs, "app/two.php:Writer.write"),
			"the conforming class declares no `write`; the same-named class beside it does, and "+
				"widening the supertype lookup must not reopen that crossing")
		require.True(t,
			ownerMemberPaired(pairs, "app/two.php:Writer.flush", "app/two.php:Server.flush"),
			"control: the member the CONFORMING class really declares still pairs, so the two "+
				"repairs compose")
	})
}
