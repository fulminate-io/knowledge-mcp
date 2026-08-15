// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// populateRepoFixture is populateFixture with a REAL RepoContext: it writes the
// fixture to a temp tree so the resolver has a root to read configs from, and
// passes the file list as the discovered set.
//
// populateFixture itself passes an empty RepoContext, which no module resolver
// can work from — an arm given no root and no file set resolves nothing, and
// every assertion below would go green-by-vacuity in the worst way.
func populateRepoFixture(t *testing.T, files []fixtureFile, extra map[string]string) PopulateResult {
	t.Helper()
	root := t.TempDir()
	var discovered []string

	write := func(rel, body string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o600))
		discovered = append(discovered, rel)
	}
	for _, f := range files {
		write(f.path, f.src)
	}
	for rel, body := range extra {
		write(rel, body)
	}

	chunker := treesitter.NewChunker()
	defer chunker.Close()

	results := make([]*treesitter.Result, 0, len(files))
	for _, f := range files {
		r, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
		require.NoError(t, err, "chunking %s", f.path)
		results = append(results, r)
	}
	return chunkResultsToPopulate("testrepo",
		&treesitter.RepoContext{Root: root, Files: discovered}, results)
}

// TestPopulate_ECMAScriptBinds drives the whole walk — capture, resolve, bind,
// emit — over MULTI-FILE fixtures, so every binding crosses a file boundary.
//
// A SINGLE-FILE FIXTURE CANNOT TELL A BIND FROM AN OWN-SCOPE HIT and would pass
// with no arm registered at all, which is why every case below imports from a
// second file.
//
// The arm is installed by jsmodule's init, which runs because this package
// imports jsmodule — so it is registered long before any fixture is chunked.
// That ordering is load-bearing rather than incidental: the chunker allocates a
// file's Binds map only when an arm is registered AT CHUNK TIME, so an arm
// installed afterwards finds nil maps and fills nothing.
func TestPopulate_ECMAScriptBinds(t *testing.T) {
	t.Run("aliased_named_import", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/lib.ts", src: "export function helper() { return 1; }\n"},
			{path: "web/main.ts", src: "" +
				"import {helper as h} from './lib';\n\n" +
				"export function run() { return h(); }\n"},
		}, nil)

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/main.ts:run", "web/lib.ts:helper"),
			"the reference writes the LOCAL alias h and must bind to the DECLARED helper")
	})

	t.Run("default_import", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/App.tsx", src: "export default function App() { return 1; }\n"},
			{path: "web/main.ts", src: "" +
				"import App from './App';\n\n" +
				"export function run() { return App(); }\n"},
		}, nil)

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/main.ts:run", "web/App.tsx:App"))
	})

	t.Run("namespace_import", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/util.ts", src: "export function fmt() { return 1; }\n"},
			{path: "web/main.ts", src: "" +
				"import * as u from './util';\n\n" +
				"export function run() { return u.fmt(); }\n"},
		}, nil)

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/main.ts:run", "web/util.ts:fmt"),
			"a namespace member keeps its own spelling and binds through the qualified rule")
	})

	t.Run("index_file", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/widgets/index.ts", src: "export function make() { return 1; }\n"},
			{path: "web/main.ts", src: "" +
				"import {make} from './widgets';\n\n" +
				"export function run() { return make(); }\n"},
		}, nil)

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/main.ts:run", "web/widgets/index.ts:make"))
	})

	t.Run("tsconfig_path", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/src/thing.ts", src: "export function thing() { return 1; }\n"},
			{path: "web/src/main.ts", src: "" +
				"import {thing} from '@app/thing';\n\n" +
				"export function run() { return thing(); }\n"},
		}, map[string]string{
			"web/tsconfig.json": `{
				/* Path aliases */
				"include": ["src"],
				"compilerOptions": {"baseUrl": ".", "paths": {"@app/*": ["src/*"]}}
			}`,
		})

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/src/main.ts:run", "web/src/thing.ts:thing"),
			"an alias specifier binds through the governing tsconfig's paths table")
	})

	t.Run("reexport_chain", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/lib/thing.ts", src: "export function thing() { return 1; }\n"},
			{path: "web/lib/index.ts", src: "export {thing} from './thing';\n"},
			{path: "web/main.ts", src: "" +
				"import {thing} from './lib';\n\n" +
				"export function run() { return thing(); }\n"},
		}, nil)

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/main.ts:run", "web/lib/thing.ts:thing"),
			"the barrel forwards; the edge lands on the DECLARING file")
	})

	// THE TICKET'S HEADLINE, ASSERTED TWO-SIDED. The negative half is what
	// proves the collision the census measured is GONE rather than relabelled:
	// the test file declares its own App (as a harness member) and the
	// reference must not land there.
	t.Run("tsx_component_test_pair", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/App.tsx", src: "export default function App() { return 1; }\n"},
			{path: "web/App.test.tsx", src: "" +
				"import App from './App';\n\n" +
				"export class Harness {\n  App() { return 2; }\n}\n\n" +
				"export function renders() { return App(); }\n"},
		}, nil)

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/App.test.tsx:renders", "web/App.tsx:App"),
			"the test's reference binds to the COMPONENT file's declaration")
		assert.False(t, hasEdge(res, kgtypes.EdgeCalls,
			"web/App.test.tsx:renders", "web/App.test.tsx:Harness.App"),
			"and never to the same-named declaration in the test file itself")

		// KNOWN-POSITIVE CONTROL: the competing declaration really exists, so
		// the negative assertion is about resolution and not about an absent node.
		assert.True(t, nodeIDSet(res)["web/App.test.tsx:Harness.App"],
			"the fixture must actually declare a competing App")
	})

	// The plain-shape control beside the terminating case below: a reference
	// imported from a bare npm specifier produces no edge, because the bind's
	// scope holds no declarations and the ladder falls through to not-declared.
	t.Run("external_module", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/main.ts", src: "" +
				"import {useState} from 'react';\n\n" +
				"export function run() { return useState(); }\n"},
		}, nil)

		assert.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "web/main.ts:run"),
			"nothing in the repository declares useState, so no edge is honest")
	})

	// THE OMISSION CATCHER, and the ONE assertion that separates a correct arm
	// from an arm that records nothing for out-of-repo imports.
	//
	//	recorded  -> the bind's scope is empty, is absent from the index's scope
	//	             set, and the external-qualifier rung TERMINATES: no edge.
	//	omitted   -> the qualified rules miss, the reference falls to the
	//	             dynamic rung, and an open-set edge lands on the LOCAL
	//	             useState — a candidate known NOT to be the referent.
	//
	// external_module above passes under BOTH implementations, so it cannot
	// substitute for this case.
	t.Run("external_qualifier_terminates", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/main.ts", src: "" +
				"import * as react from 'react';\n\n" +
				"export function useState() { return 0; }\n\n" +
				"export function run() { return react.useState(); }\n"},
		}, nil)

		assert.False(t, hasEdge(res, kgtypes.EdgeCalls, "web/main.ts:run", "web/main.ts:useState"),
			"a qualified reference through an out-of-repo module must not bind to a "+
				"same-named LOCAL declaration")

		// KNOWN-POSITIVE CONTROL: the local declaration exists and is indexed,
		// so the absence above is a resolution decision rather than a missing node.
		assert.True(t, nodeIDSet(res)["web/main.ts:useState"],
			"the fixture must actually declare a local useState")
	})

	// THE RATIFIED ALIASING CATCHER, REQUIRED. Its reference sits INSIDE A
	// PARENTED DECLARATION — a class method — and a parented reference site is
	// a BY-VALUE copy of the file-level one taken during chunking. A binds pass
	// that ASSIGNED a fresh map would update the file-level site only, leaving
	// every parented reference with the nil map it copied; every other subtest
	// here passes under that defect because their references sit at file level.
	t.Run("binding_inside_parented_decl", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/lib.ts", src: "export function helper() { return 1; }\n"},
			{path: "web/main.ts", src: "" +
				"import {helper} from './lib';\n\n" +
				"export class Runner {\n" +
				"  run() { return helper() + other(); }\n" +
				"  other() { return 1; }\n" +
				"}\n"},
		}, nil)

		assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/main.ts:Runner.run", "web/lib.ts:helper"),
			"the bind must reach a reference emitted from INSIDE a class method")

		// SIBLING HALF, INVERTED — AND IT WAS ASSERTING AN EDGE THE LANGUAGE
		// DOES NOT HAVE. It previously required Runner.run -> Runner.other to
		// bind, commented "and the parented site still resolves its own
		// siblings". A bare `other()` inside a class method is not a call on
		// this in ECMAScript: measured with node, exactly this shape reports
		//   ReferenceError: a is not defined
		// so the edge encoded a ReferenceError as a resolved call. The
		// per-language sibling gate did not break that assertion, it EXPOSED it.
		//
		// THE ALIASING CATCHER ABOVE IS UNTOUCHED AND LOSES NOTHING. Its job is
		// to prove a bind reaches a reference emitted from inside a class method
		// whose RefSite is a by-value copy — the first assertion already does
		// that on its own, and it is the ratified half.
		assert.False(t, hasEdge(res, kgtypes.EdgeCalls,
			"web/main.ts:Runner.run", "web/main.ts:Runner.other"),
			"a bare sibling call is a ReferenceError in ECMAScript, so no edge is honest")

		// KNOWN-POSITIVE CONTROL for that absence: the sibling member is
		// genuinely declared and indexed, so the missing edge is a resolution
		// decision rather than a node the fixture never produced.
		assert.True(t, nodeIDSet(res)["web/main.ts:Runner.other"],
			"the fixture must actually declare the sibling member")
	})
}
