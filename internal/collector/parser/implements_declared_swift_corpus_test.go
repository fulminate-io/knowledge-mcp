// SPDX-License-Identifier: Apache-2.0

// IT IS AN EXTERNAL TEST PACKAGE ON PURPOSE, for the reason its sibling C
// precision measurement records: a corpus root taken from the ENVIRONMENT is a
// taint source, and read inside package parser it flows into Populate's own file
// walk and file reads, where the path-traversal analyzer reports at the
// production sink. Everything this test needs is exported, so sitting outside
// the package removes the flow instead of annotating a production file for a
// test's benefit.
package parser_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestSwiftConformanceCorpusYield measures what the supertype narrowing actually
// yields on real source, and what it does OUTSIDE swift.
//
// THE FIXTURES PROVE THE SHAPE IS CAPTURED. They cannot prove a yield, because a
// fixture only contains what its author thought to write. This is the instrument
// that meets real source, and the strata it prints are what a reviewer reads.
//
// THE MEASUREMENT COMES FROM PRODUCTION, NOT FROM THIS TEST. conformanceLog
// emits the whole counter line through slog, which `go test -v` captures, so the
// numbers a reader checks are the derivation's own account rather than a total
// this file recomputed for itself.
//
// EACH SUBTEST READS ITS OWN ROOT AND SKIPS ON ITS OWN, so either can be
// selected and run alone. A SKIP NAMES THE VARIABLE and prints no PASS line, so
// a criterion greping for one cannot be satisfied by an absent corpus.
func TestSwiftConformanceCorpusYield(t *testing.T) {
	t.Run("swift_yield", func(t *testing.T) {
		root := os.Getenv("FUL1432_SWIFT_ROOT")
		if root == "" {
			t.Skip("FUL1432_SWIFT_ROOT is unset: this measurement needs a real swift corpus and reports nothing without one")
		}
		require.DirExistsf(t, root, "FUL1432_SWIFT_ROOT names %q, which is not a directory", root)

		res, err := parser.Populate(context.Background(), "ful1432swift", root)
		require.NoError(t, err)

		typeLevel, memberLevel := swiftYieldStrata(res, ".swift")
		t.Logf("swift declared conformance: type_level=%d member_level=%d total=%d",
			typeLevel, memberLevel, typeLevel+memberLevel)
		require.Positive(t, typeLevel,
			"the swift corpus must yield type-level declared-conformance edges, or the narrowing's yield is unmeasured rather than zero")
	})

	t.Run("control_zero", func(t *testing.T) {
		// THE CONTROL'S ZERO IS ONLY EVIDENCE BESIDE A POSITIVE. An empty,
		// unwalked or unparsed corpus prints reopened_supertype=0 exactly as an
		// inert one does, so a bare zero cannot distinguish "the narrowing does
		// not fire outside swift" — the blast-radius claim — from "nothing was
		// measured". This subtest therefore requires a conformance count of its
		// own, and its criterion carries a matching positive-supertypes leg
		// beside the zero.
		root := os.Getenv("FUL1432_CTL_ROOT")
		if root == "" {
			t.Skip("FUL1432_CTL_ROOT is unset: this control needs a real non-swift corpus and reports nothing without one")
		}
		require.DirExistsf(t, root, "FUL1432_CTL_ROOT names %q, which is not a directory", root)

		res, err := parser.Populate(context.Background(), "ful1432ctl", root)
		require.NoError(t, err)

		typeLevel, memberLevel := swiftYieldStrata(res, "")
		t.Logf("control declared conformance: type_level=%d member_level=%d total=%d",
			typeLevel, memberLevel, typeLevel+memberLevel)
		require.Positive(t, typeLevel,
			"the control corpus must yield declared-conformance edges of its own, or its reopened_supertype=0 says nothing about the narrowing being inert")
	})
}

// swiftYieldStrata counts the declared-conformance relationships whose SOURCE
// lives in a file with the given extension, split into the two levels. An empty
// extension counts every file.
//
// THE SPLIT IS READ OFF THE NODE ID rather than tracked alongside it: a member
// declaration's ID is container-qualified `<file>:<Container>.<member>`, so a dot
// in the symbol half is what tells a member relationship from a type-level one.
func swiftYieldStrata(res parser.PopulateResult, ext string) (typeLevel, memberLevel int) {
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements {
			continue
		}
		if !strings.HasPrefix(e.Method, kgtypes.EdgeMethodDeclaredConformance) {
			continue
		}
		file, symbol := swiftYieldSplitNodeID(e.FromId)
		if ext != "" && !strings.HasSuffix(file, ext) {
			continue
		}
		if strings.Contains(symbol, ".") {
			memberLevel++
			continue
		}
		typeLevel++
	}
	return typeLevel, memberLevel
}

// swiftYieldSplitNodeID splits a node ID at its LAST colon — a path may contain
// none, but a declaration node ID always ends with ":<symbol>".
func swiftYieldSplitNodeID(nodeID string) (file, symbol string) {
	i := strings.LastIndex(nodeID, ":")
	if i < 0 {
		return nodeID, ""
	}
	return nodeID[:i], nodeID[i+1:]
}
