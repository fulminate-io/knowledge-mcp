// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// containmentCase is one fixture corpus: a set of files walked together, plus
// the same-name collision the pair is built to provoke.
type containmentCase struct {
	name  string
	files []fixtureFile
}

// nodesByID indexes a populate result's nodes so an edge endpoint can be
// resolved back to the node it names.
func nodesByID(res PopulateResult) map[string]*knowledgev1.Node {
	byID := make(map[string]*knowledgev1.Node, len(res.Nodes))
	for _, n := range res.Nodes {
		byID[n.Id] = n
	}
	return byID
}

// fileNodeIDs is the set of node IDs that are NodeFile nodes.
func fileNodeIDs(res PopulateResult) map[string]bool {
	out := make(map[string]bool)
	for _, n := range res.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeFile {
			out[n.Id] = true
		}
	}
	return out
}

// crossFileCollisionCases are the empirically-anchored rows from the 844-edge
// cross-file CONTAINS census. Every pair declares the SAME name in two files
// and the two files are deliberately NOT byte-identical — asserted below, not
// assumed, because four of five real offending pairs were measured as different
// SIZES. A fixture that drifted into two identical files would be exercising a
// content-dedup story rather than the collision this ticket fixes.
//
// EVERY FILE ALSO PRODUCES AT LEAST ONE ORPHAN CHUNK, and the yaml case is
// ORPHAN-ONLY. Without an orphan-producing shape the whole corpus would pass
// while the orphan-containment fix never ran.
var crossFileCollisionCases = []containmentCase{
	{
		// Highest-yield census shape: 370 of 844. A component and its
		// CO-LOCATED test file in one directory, both declaring the name.
		name: "tsx_component_and_colocated_test",
		files: []fixtureFile{
			{path: "web/Banner.tsx", src: "" +
				"const PALETTE = { bg: 'red', fg: 'white' };\n\n" +
				"export function Banner() {\n  return <div className={PALETTE.bg}>hi</div>;\n}\n"},
			{path: "web/Banner.test.tsx", src: "" +
				"const FIXTURE_PROPS = { title: 'a banner title', dismissable: true };\n\n" +
				"export function Banner() {\n  return null;\n}\n"},
		},
	},
	{
		// Same-package init: two files in one directory both declaring
		// func init(), with different bodies.
		name: "go_same_package_init",
		files: []fixtureFile{
			{path: "svc/a.go", src: "" +
				"package svc\n\nconst aRetries, aTimeoutSeconds = 3, 30\n\nfunc init() {\n\tprintln(\"a\")\n}\n"},
			{path: "svc/b.go", src: "" +
				"package svc\n\nvar bEnabled, bLabel = true, \"beta\"\n\nfunc init() {\n\tprintln(\"b\", \"second\")\n}\n"},
		},
	},
	{
		// The row the directory-basename rule cannot explain and the package
		// clause can: two `package main` files in DIFFERENT directories both
		// declaring func main(). It is why Go's scope unit is the directory.
		name: "go_cross_directory_package_main",
		files: []fixtureFile{
			{path: "cmd/alpha/main.go", src: "" +
				"package main\n\nconst alphaBanner = \"alpha starting up now\"\n\nfunc main() {\n\tprintln(alphaBanner)\n}\n"},
			{path: "cmd/beta/main.go", src: "" +
				"package main\n\nvar betaFlag, betaOther = \"beta\", \"other value here\"\n\nfunc main() {\n\tprintln(betaFlag, betaOther)\n}\n"},
		},
	},
	{
		name: "typescript_two_e2e_fixtures",
		files: []fixtureFile{
			{path: "e2e/one.fixture.ts", src: "" +
				"import { setup } from './harness';\n\nexport const client = { base: 'http://localhost:8080', retries: 3 };\n"},
			{path: "e2e/two.fixture.ts", src: "" +
				"import { teardown } from './harness';\n\nexport const client = { base: 'http://example.test', retries: 9, verbose: true };\n"},
		},
	},
	{
		name: "bash_two_test_scripts",
		files: []fixtureFile{
			{path: "scripts/a.test.sh", src: "" +
				"set -euo pipefail\necho \"starting the first test script now\"\n\nfail() {\n  echo \"a failed\" >&2\n  exit 1\n}\n"},
			{path: "scripts/b.test.sh", src: "" +
				"set -euo pipefail\necho \"starting the second test script now\"\n\nfail() {\n  echo \"b failed with a different body\" >&2\n  exit 2\n}\n"},
		},
	},
	{
		name: "javascript_two_mjs_scripts",
		files: []fixtureFile{
			{path: "tools/one.mjs", src: "" +
				"import path from 'node:path';\n\nexport const scriptDir = path.dirname('/tools/one.mjs');\n"},
			{path: "tools/two.mjs", src: "" +
				"import path from 'node:path';\n\nexport const scriptDir = path.resolve('/tools', 'two', 'nested');\n"},
		},
	},
	{
		name: "python_two_scripts",
		files: []fixtureFile{
			{path: "bin/one.py", src: "" +
				"import os, sys\n\ndef main():\n    return os.getcwd()\n"},
			{path: "bin/two.py", src: "" +
				"import json, sys\n\ndef main():\n    return json.dumps({'a': 1, 'b': 2})\n"},
		},
	},
}

// TestInvariant_NoCrossFileContains is the permanent replacement for the
// throwaway cross-file edge probe.
//
// THE INVARIANT: for every CONTAINS edge whose FromID names a NodeFile node,
// the target node's FilePath equals the source file node's FilePath. Zero
// exceptions.
//
// SCOPE NOTE, deliberate and not an oversight: this is FILE-to-symbol
// containment only. A parent-to-member CONTAINS edge that crosses files inside
// one Go package is CORRECT — a receiver type genuinely lives in another file
// of its package — and is not a violation. Widening this test to symbol-source
// edges would red it against correct work.
func TestInvariant_NoCrossFileContains(t *testing.T) {
	for _, tc := range crossFileCollisionCases {
		t.Run(tc.name, func(t *testing.T) {
			// The collision must be between DIFFERENT files. Asserted rather
			// than assumed, so a future edit that accidentally makes the pair
			// identical fails loudly instead of testing something else.
			require.Len(t, tc.files, 2, "each census-anchored case is a PAIR")
			require.NotEqual(t, tc.files[0].src, tc.files[1].src,
				"fixture pair must not be byte-identical — the census measured real pairs as differing in size")

			res := populateFixture(t, tc.files)

			byID := nodesByID(res)
			isFile := fileNodeIDs(res)
			require.Len(t, isFile, len(tc.files),
				"every fixture file must produce a NodeFile node")

			// KNOWN-POSITIVE CONTROL: without it, a walk that produced no
			// containment at all would satisfy the zero below perfectly.
			// Counted against the FIXTURE-DERIVED file count, never against
			// anything derived from the result set.
			sameFileContains := map[string]int{}

			for _, e := range res.Edges {
				if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains {
					continue
				}
				if !isFile[e.FromId] {
					continue // symbol-source containment; out of scope here
				}
				target, ok := byID[e.ToId]
				require.True(t, ok,
					"CONTAINS edge from file %q points at unknown node %q", e.FromId, e.ToId)

				srcFile := byID[e.FromId]
				require.Equal(t, srcFile.FilePath, target.FilePath,
					"CROSS-FILE CONTAINS: file %q contains %q which lives in %q",
					e.FromId, e.ToId, target.FilePath)

				sameFileContains[e.FromId]++
			}

			for _, f := range tc.files {
				require.Positive(t, sameFileContains[f.path],
					"control failed: file %q produced no file-to-symbol CONTAINS edge at all", f.path)
			}
		})
	}
}

// orphanContainmentCases cover the three distinct causes of an uncontained
// chunk node. They are distinguished by PROVENANCE, not by nesting depth: the
// TopLevel query matches at any depth, so an unnamed DECLARATION chunk can be
// nested inside a function body while collectOrphans only walks the root's
// named children.
var orphanContainmentCases = []containmentCase{
	{
		// ORPHAN-ONLY, and the largest live population (yaml 4,608). This
		// language names no chunks at all, so every node it produces is an
		// orphan and the whole file rides on the orphan fix.
		name: "orphans_yaml_document",
		files: []fixtureFile{
			{path: "deploy/app.yaml", src: "" +
				"name: my-application\nspec:\n  replicas: 3\n  image: registry.example.com/app:v1.2.3\n  ports:\n    - 8080\n    - 9090\n"},
		},
	},
	{
		// ORPHANS from a Go top-level const/var block: live population 780
		// const_declaration plus 765 var_declaration.
		name: "orphans_go_const_and_var_block",
		files: []fixtureFile{
			{path: "cfg/values.go", src: "" +
				"package cfg\n\n" +
				"const (\n\tRetries        = 5\n\tTimeoutSeconds = 30\n\tLabelPrefix    = \"knowledge\"\n)\n\n" +
				"var (\n\tEnabled  = true\n\tFallback = \"none at all here\"\n)\n\n" +
				"func Use() int { return Retries }\n"},
		},
	},
	{
		// ORPHANS from a bash bare command line: live population 664.
		name: "orphans_bash_bare_commands",
		files: []fixtureFile{
			{path: "scripts/run.sh", src: "" +
				"set -euo pipefail\necho \"preparing the working directory now\"\nmkdir -p /tmp/knowledge-run-dir\n\n" +
				"cleanup() {\n  rm -rf /tmp/knowledge-run-dir\n}\n"},
		},
	},
	{
		// UNNAMED DECLARATIONS — the edge was always emitted, but qualifiedName
		// returns "" for a nameless chunk so the ToID was empty and resolution
		// dropped it.
		name: "unnamed_declarations_c",
		files: []fixtureFile{
			{path: "src/lib.c", src: "" +
				"#include <stdio.h>\n\n" +
				"struct { int a; int b; } anon_instance_value;\n\n" +
				"int compute(int x) {\n  return x * 2;\n}\n"},
		},
	},
	{
		// UNNAMED TEST BLOCKS — same empty-ToID shape, reached when neither
		// @name nor firstStringArg produced a label.
		name: "unnamed_test_blocks_ts",
		files: []fixtureFile{
			{path: "spec/suite.spec.ts", src: "" +
				"import { describe, it } from 'vitest';\n\n" +
				"describe(topicName, () => {\n  it(caseName, () => {\n    expect(1).toBe(1);\n  });\n});\n"},
		},
	},
}

// TestInvariant_EveryChunkNodeIsContained pins the CEO ruling that a chunk with
// no file "doesnt make sense": every node that is neither a NodeFile nor a
// NodeLanguage is the target of exactly one CONTAINS edge from its OWN file.
//
// COMMENT CHUNKS ARE CORRECTLY OUT OF SCOPE. populate.go folds a comment
// chunk's text into the following symbol's Description and never creates a node
// for it, so a comment produces no node and needs no edge. The invariant is
// over NODES, which is why nothing here counts chunks.
func TestInvariant_EveryChunkNodeIsContained(t *testing.T) {
	for _, tc := range orphanContainmentCases {
		t.Run(tc.name, func(t *testing.T) {
			res := populateFixture(t, tc.files)

			byID := nodesByID(res)
			isFile := fileNodeIDs(res)
			require.Len(t, isFile, len(tc.files), "every fixture file must produce a NodeFile node")

			// Count file-to-symbol containment per target so "exactly one" is
			// checkable, and so a double-contained node fails too.
			containedBy := map[string][]string{}
			for _, e := range res.Edges {
				if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains {
					continue
				}
				if !isFile[e.FromId] {
					continue
				}
				containedBy[e.ToId] = append(containedBy[e.ToId], e.FromId)
			}

			// KNOWN-POSITIVE CONTROL against a FIXTURE-DERIVED expectation:
			// asserting len(nodes) == len(containsEdges) is the identity that
			// passes when both are zero, so the subject node count is required
			// to be positive on its own terms first.
			subjects := 0
			for _, n := range res.Nodes {
				nt := kgtypes.NodeType(n.Type)
				if nt == kgtypes.NodeFile || nt == kgtypes.NodeLanguage {
					continue
				}
				subjects++

				sources := containedBy[n.Id]
				require.Len(t, sources, 1,
					"node %q (type %s, file %s) must be contained by exactly one file node, got %v",
					n.Id, n.Type, n.FilePath, sources)
				require.Equal(t, n.FilePath, byID[sources[0]].FilePath,
					"node %q is contained by the wrong file", n.Id)
			}
			require.Positive(t, subjects,
				"control failed: fixture produced no chunk nodes, so the containment assertion above was vacuous")
		})
	}
}
