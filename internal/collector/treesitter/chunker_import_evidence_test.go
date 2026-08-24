// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunker_import_evidence_test.go covers the per-site group key IMPORTS edges
// now carry, and the two properties it exists for.
//
// THE DEFECT IT CLOSES, measured on this repository rather than imagined. Two
// import constructs naming ONE specifier used to emit two edges byte-identical
// in every hashed field, because the emission loop set only FromID, ToID and
// Type. The edges identity is UNIQUE over (from_id, to_id, type,
// COALESCE(evidence,'')), so the server could store only one of them while the
// client's per-file contribution hash folded both — the file's hash could never
// agree and it re-uploaded on every collect, forever. The two live examples were
// a Go file importing one path plainly and again under an alias, and a Python
// file carrying two from-imports of one module.

// importEdgesOf chunks one source and returns its IMPORTS edges.
func importEdgesOf(t *testing.T, path, src string) []Edge {
	t.Helper()
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), path, []byte(src))
	require.NoError(t, err, "chunking %s", path)
	var out []Edge
	for _, e := range res.Edges {
		if e.Type == EdgeImports {
			out = append(out, e)
		}
	}
	return out
}

// TestImportEdges_PerSiteEvidenceDistinguishesDuplicateImports drives BOTH known
// duplicate-import shapes and requires each file's two sites to be two distinct
// edge identities.
func TestImportEdges_PerSiteEvidenceDistinguishesDuplicateImports(t *testing.T) {
	// IDENTITY IS THE FOUR-PART TUPLE, so that is what is compared — not the
	// evidence string alone. Two sites are correctly separated when their full
	// identities differ, however that separation is achieved.
	identities := func(edges []Edge) map[string]int {
		out := map[string]int{}
		for _, e := range edges {
			out[e.FromID+"|"+e.ToID+"|"+string(e.Type)+"|"+e.Evidence]++
		}
		return out
	}

	t.Run("go_plain_and_aliased_import_of_one_path", func(t *testing.T) {
		const src = "package tools\n\n" +
			"import (\n" +
			"\t\"github.com/x/logs\"\n" +
			"\tcollectorlogs \"github.com/x/logs\"\n" +
			")\n\n" +
			"func Use() { _ = logs.A; _ = collectorlogs.B }\n"
		edges := importEdgesOf(t, "tools/tools_logs_traverse.go", src)
		require.Len(t, edges, 2, "the file imports one path TWICE, so it must emit two import edges")

		ids := identities(edges)
		require.Len(t, ids, 2,
			"the two sites must be two distinct four-part identities, or the schema stores one "+
				"and the file's contribution hash can never agree: %v", ids)
		for id, n := range ids {
			assert.Equal(t, 1, n, "identity %q must be emitted exactly once", id)
		}
		// The alias is what separates them here, so no ordinal is needed: both
		// sites take n=0 under their own discriminator.
		assert.Equal(t, map[string]int{"import::0": 1, "import:collectorlogs:0": 1},
			evidenceCounts(edges), "the local name is the discriminator for this shape")
	})

	t.Run("python_two_from_imports_of_one_module", func(t *testing.T) {
		const src = "" +
			"from criterion_lib import gate\n" +
			"from criterion_lib import hygiene\n" +
			"\n" +
			"def run():\n" +
			"    return gate, hygiene\n"
		edges := importEdgesOf(t, "scripts/criterion_hygiene_gates.py", src)

		// Python has NO registered import arm, so no local name is known for
		// either site — which is exactly why an alias-keyed scheme would not have
		// worked and the ordinal has to exist.
		var oneModule []Edge
		for _, e := range edges {
			if e.ToID == "criterion_lib" {
				oneModule = append(oneModule, e)
			}
		}
		require.Len(t, oneModule, 2,
			"the file carries two from-imports of one module, so two edges name it; got %d", len(oneModule))

		ids := identities(oneModule)
		require.Len(t, ids, 2,
			"two sites identical in every recorded respect must still be two identities — "+
				"this is the case the ordinal exists for: %v", ids)
		assert.Equal(t, map[string]int{"import::0": 1, "import::1": 1}, evidenceCounts(oneModule),
			"with no local known, the ordinal is what separates them, and the doubled colon "+
				"must not be collapsed or the ordinal stops being the final field")
	})
}

// evidenceCounts tallies the Evidence values across a set of edges.
func evidenceCounts(edges []Edge) map[string]int {
	out := map[string]int{}
	for _, e := range edges {
		out[e.Evidence]++
	}
	return out
}

// TestImportEdges_EvidenceUnchangedWhenBytesShift is the position-independence
// guard. A key derived from a line, column or byte offset would re-stamp every
// import below an edit, minting an orphan per site on every ordinary edit — the
// defect the reference key already had and this format must not reproduce.
func TestImportEdges_EvidenceUnchangedWhenBytesShift(t *testing.T) {
	const base = "package svc\n\n" +
		"import (\n\t\"fmt\"\n\tal \"os\"\n)\n\n" +
		"func Use() { fmt.Println(al.Args) }\n"
	// The SAME imports, pushed down the file by text inserted above them, and
	// indented differently. Every byte offset, line and column moves.
	const shifted = "package svc\n\n" +
		"// A leading comment block that did not exist before.\n" +
		"// Second line of it.\n" +
		"\n" +
		"import (\n\t\"fmt\"\n\tal \"os\"\n)\n\n" +
		"func Use() { fmt.Println(al.Args) }\n"

	before := evidenceCounts(importEdgesOf(t, "svc/a.go", base))
	after := evidenceCounts(importEdgesOf(t, "svc/a.go", shifted))
	require.NotEmpty(t, before, "control: the fixture must emit keyed import edges at all")
	assert.Equal(t, before, after,
		"inserting text ABOVE the import block moves every offset in the file and must leave "+
			"every import's group key untouched — a re-keyed edge is a NEW identity, so the "+
			"pre-edit row is orphaned and the file stops converging")

	// KNOWN-POSITIVE CONTROL: the key is not a constant. Changing what the site
	// actually IS — here the bound local name — must change it.
	const renamed = "package svc\n\n" +
		"import (\n\t\"fmt\"\n\tother \"os\"\n)\n\n" +
		"func Use() { fmt.Println(other.Args) }\n"
	assert.NotEqual(t, before, evidenceCounts(importEdgesOf(t, "svc/a.go", renamed)),
		"renaming the local binding must change the key, or the stability assertion above is "+
			"vacuous — it would be satisfied by a key that can never change")

	// And no positional token reaches the key under any of the three.
	positional := regexp.MustCompile(`^import:[^:]*:\d+$`)
	for key := range after {
		assert.Regexp(t, positional, key,
			"the key must be exactly import:<local>:<ordinal>, with the ordinal LAST: %q", key)
	}
}
