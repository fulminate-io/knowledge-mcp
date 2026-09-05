// SPDX-License-Identifier: Apache-2.0

package contribhash

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// vector_test.go — the CLIENT third of the three-way parity vector.
//
// testdata/contribution_hash_vector.json at the repo root is a DATA file, not a
// package: cmd/knowledge and cmd/knowledge-server are separate modules and
// AGENTS.md denies any hand-written package shared between them, so a shared
// FIXTURE is the only legal shape. Its expected hashes were authored FROM
// docs/collect-contribution-hash.md rather than produced by running any of the
// three implementations — a vector generated from one implementation would only
// prove the other two agree with IT.
//
// WHAT THIS TEST CANNOT CATCH, stated so the parity claim is not read as wider
// than it is: the collation case's ORDERING is byte-wise in Go by construction,
// so only the CLOUD test can catch a missing COLLATE "C". And a Go zero value IS
// the absent encoding, so the empty-string half of null_vs_empty is not
// constructible here; this test asserts the FILE's two hashes differ and leaves
// the empty-string hash itself to the cloud test.

type vecNode struct {
	ID         string  `json:"id"`
	Type       *string `json:"type"`
	SymbolName *string `json:"symbol_name"`
	FilePath   *string `json:"file_path"`
	Language   *string `json:"language"`
	StartLine  *int32  `json:"start_line"`
	EndLine    *int32  `json:"end_line"`
	Content    *string `json:"content"`
	Signature  *string `json:"signature"`
	IsExported *bool   `json:"is_exported"`
	IsTest     *bool   `json:"is_test"`
	TestKind   *string `json:"test_kind"`
	Desc       *string `json:"description"`
	Source     *string `json:"source"`
	Status     *string `json:"status"`
	Hash       string  `json:"hash"`
	CloudOnly  bool    `json:"cloud_only"`
}

type vecEdge struct {
	FromID     string   `json:"from_id"`
	ToID       string   `json:"to_id"`
	Type       string   `json:"type"`
	Weight     *float64 `json:"weight"`
	Confidence *float64 `json:"confidence"`
	Method     *string  `json:"method"`
	Evidence   *string  `json:"evidence"`
	Hash       string   `json:"hash"`
}

type vecCase struct {
	Label    string    `json:"label"`
	Kind     string    `json:"kind"`
	Nodes    []vecNode `json:"nodes"`
	Edges    []vecEdge `json:"edges"`
	FileHash string    `json:"file_hash"`
}

type vecDoc struct {
	SchemeVersion uint32    `json:"scheme_version"`
	Cases         []vecCase `json:"cases"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func boolean(p *bool) bool {
	return p != nil && *p
}

func f64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// sharedTestdataPath resolves a file in the SHARED testdata directory by
// walking up from this package to the first ancestor that carries BOTH a
// go.mod and that file under testdata/.
//
// NO FIXED ".." COUNT SPELLS BOTH LAYOUTS. The fixture is read by a test in
// each of the two modules, so it sits ABOVE both module roots here; the
// published mirror is a single module whose root carries testdata/ directly,
// because the sync script copies cmd/knowledge/internal to internal/ and the
// shared testdata tree to the mirror root. Walking for the artifact itself
// survives both layouts and a package move, and fails loudly rather than
// silently comparing against nothing.
func sharedTestdataPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			candidate := filepath.Join(dir, "testdata", name)
			if _, statErr := os.Stat(candidate); statErr == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir,
			"walked to the filesystem root from the test working directory without finding testdata/%s beside a go.mod", name)
		dir = parent
	}
}

func loadVector(t *testing.T) vecDoc {
	t.Helper()
	raw, err := os.ReadFile(sharedTestdataPath(t, "contribution_hash_vector.json"))
	require.NoError(t, err)
	var doc vecDoc
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Cases, "control: the vector must carry cases — an empty file would pass every assertion below vacuously")
	return doc
}

func (n vecNode) proto() *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: n.ID, Type: str(n.Type), SymbolName: str(n.SymbolName), FilePath: str(n.FilePath),
		Language: str(n.Language), StartLine: i32(n.StartLine), EndLine: i32(n.EndLine),
		Content: str(n.Content), Signature: str(n.Signature), IsExported: boolean(n.IsExported),
		IsTest: boolean(n.IsTest), TestKind: str(n.TestKind), Description: str(n.Desc),
		Source: str(n.Source), Status: str(n.Status),
	}
}

func (e vecEdge) batch() kgwire.BatchEdge {
	return kgwire.BatchEdge{
		FromIdx: -1, ToIdx: -1,
		FromID: e.FromID, ToID: e.ToID, Type: kgtypes.EdgeType(e.Type),
		Weight: f64(e.Weight), Confidence: f64(e.Confidence),
		Method: str(e.Method), Evidence: str(e.Evidence),
	}
}

// TestContributionHash_ParityVector asserts the client implementation reproduces
// every spec-authored hash in the vector it can represent, and that the
// must-differ pairs do differ.
func TestContributionHash_ParityVector(t *testing.T) {
	doc := loadVector(t)
	require.Equal(t, ContributionHashSchemeVersion, doc.SchemeVersion,
		"the vector was authored for a different scheme version than this client declares")

	var checked int
	for _, c := range doc.Cases {
		t.Run(c.Label, func(t *testing.T) {
			for _, n := range c.Nodes {
				if n.CloudOnly {
					continue
				}
				got := NodeContributionHash(n.proto())
				require.Equal(t, n.Hash, hex.EncodeToString(got[:]),
					"node %q hash disagrees with the spec-authored vector", n.ID)
				checked++
			}
			for _, e := range c.Edges {
				got := EdgeContributionHash(e.batch())
				require.Equal(t, e.Hash, hex.EncodeToString(got[:]),
					"edge %s->%s/%s hash disagrees with the spec-authored vector", e.FromID, e.ToID, e.Type)
				checked++
			}
			switch c.Kind {
			case "nodes_must_differ":
				require.Len(t, c.Nodes, 2, "a must-differ node case needs exactly two rows")
				require.NotEqual(t, c.Nodes[0].Hash, c.Nodes[1].Hash,
					"the vector itself must record differing hashes for %s", c.Label)
			case "edges_must_differ":
				require.Len(t, c.Edges, 2, "a must-differ edge case needs exactly two rows")
				require.NotEqual(t, c.Edges[0].Hash, c.Edges[1].Hash,
					"the vector itself must record differing hashes for %s", c.Label)
			case "file_aggregate":
				nodes := make([]*knowledgev1.Node, 0, len(c.Nodes))
				for _, n := range c.Nodes {
					nodes = append(nodes, n.proto())
				}
				edges := make([]kgwire.BatchEdge, 0, len(c.Edges))
				for _, e := range c.Edges {
					edges = append(edges, e.batch())
				}
				byFile := FileContributionHashes(nodes, edges)
				require.Len(t, byFile, 1, "the %s case is authored as one file group", c.Label)
				for _, got := range byFile {
					require.Equal(t, c.FileHash, hex.EncodeToString(got[:]),
						"the %s file aggregate disagrees with the spec-authored vector", c.Label)
				}
				checked++
			}
		})
	}
	// KNOWN-POSITIVE CONTROL: a vector whose rows all failed to load, or a loop
	// that silently skipped every row, would leave this at zero and every
	// assertion above unexecuted.
	require.Positive(t, checked, "control: no vector row was actually hashed")
}
