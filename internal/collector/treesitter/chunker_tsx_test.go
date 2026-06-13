// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// jsxComponentSrc is a JSX-returning React component in the route-tree shape
// from the original bug report. Under the plain `typescript` grammar the
// `<Routes>`/`<Route .../>` JSX lexes as type/comparison syntax and derails
// the parse into ERROR nodes; under the JSX-capable `tsx` grammar it parses
// cleanly.
const jsxComponentSrc = `import { Routes, Route } from "react-router-dom";
import { Home } from "./Home";

export function App() {
  return (
    <Routes>
      <Route path="/" element={<Home />} />
      <Route path="/about" element={<div>About</div>} />
    </Routes>
  );
}
`

// TestTSX_JSXParsesWithoutErrorNodes is the core regression: the JSX-bearing
// component parses with ZERO ERROR nodes under LangTSX (the fix) while the
// SAME source parses WITH ERROR nodes under LangTypeScript (the bug). The
// both-arm assertion documents the bug and proves the test exercises real
// JSX — a non-JSX fixture would parse clean under both grammars and the
// LangTypeScript arm would not go red.
func TestTSX_JSXParsesWithoutErrorNodes(t *testing.T) {
	parser := NewParser()
	defer parser.Close()

	// Fix arm: tsx grammar parses JSX cleanly.
	tsxTree, err := parser.Parse(context.Background(), []byte(jsxComponentSrc), LangTSX)
	if err != nil {
		t.Fatalf("parse under LangTSX: %v", err)
	}
	defer tsxTree.Close()
	if tsxTree.RootNode().HasError() {
		t.Errorf("LangTSX parse of JSX has ERROR nodes; expected a clean parse")
	}

	// Bug arm: the plain typescript grammar derails on JSX. Asserting the
	// error here keeps the regression honest — if a future grammar bump made
	// `typescript` JSX-capable, this arm would fail loudly and prompt review.
	tsTree, err := parser.Parse(context.Background(), []byte(jsxComponentSrc), LangTypeScript)
	if err != nil {
		t.Fatalf("parse under LangTypeScript: %v", err)
	}
	defer tsTree.Close()
	if !tsTree.RootNode().HasError() {
		t.Errorf("LangTypeScript parse of JSX has NO ERROR nodes; expected the " +
			"JSX-incapable grammar to derail (the test must exercise real JSX)")
	}
}

// TestTSX_ComponentLandsAsSymbolChunk asserts ChunkFile on a .tsx path with a
// JSX component produces at least one non-import symbol chunk named "App".
// Fails-when-absent: under the old .tsx->typescript mapping the JSX-bearing
// function_declaration is dropped into ERROR-soup, leaving only import
// chunks. The DetectLanguage(".tsx")==LangTSX routing inside ChunkFile is
// what makes this pass.
func TestTSX_ComponentLandsAsSymbolChunk(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	res, err := chunker.ChunkFile(context.Background(), "components/App.tsx", []byte(jsxComponentSrc))
	if err != nil {
		t.Fatalf("ChunkFile(App.tsx): %v", err)
	}

	var seen []string
	found := false
	for _, c := range res.Chunks {
		seen = append(seen, c.ChunkType+":"+c.Name)
		// A non-import symbol chunk: the function declaration for App, not an
		// import_statement and not a test_block.
		if c.Name == "App" && c.ChunkType != "import_statement" && c.ChunkType != "test_block" {
			found = true
		}
	}
	if !found {
		t.Errorf("no non-import symbol chunk named %q; got chunks: %v", "App", seen)
	}
}
