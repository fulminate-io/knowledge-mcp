// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// documentRootOf returns the single "document" node from an emitted slice.
func documentRootOf(t *testing.T, nodes []*knowledgev1.Node) *knowledgev1.Node {
	t.Helper()
	var found *knowledgev1.Node
	for _, n := range nodes {
		if n.Type == "document" {
			require.Nil(t, found, "more than one document root emitted; the assertions below would read an arbitrary one")
			found = n
		}
	}
	require.NotNil(t, found, "no document root emitted; every assertion below would be vacuous")
	return found
}

// TestEmit_CollectStampIsTheRunInstant fences collected_at on the pdf document
// root as the instant THE COLLECT RAN, distinct from the two dates the PDF
// carries about itself.
//
// A PDF's CreationDate and ModDate describe the document and are frequently
// years old; neither says anything about how stale the raw graph is. The
// wrong-but-compiling implementations this separates are: plumbing the
// parameter through and never writing it, aliasing one of the PDF's own dates,
// and hardcoding a constant.
//
// KNOWN POSITIVE ON THE SAME READ PATH: creation_date and mod_date are
// asserted PRESENT in the same metadata map the stamp is read from, so
// "collected_at is neither of them" is measured rather than inferred from two
// absent keys.
func TestEmit_CollectStampIsTheRunInstant(t *testing.T) {
	t.Parallel()

	runInstant := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	meta := pdf.Metadata{
		Title:        "DDIA",
		CreationDate: time.Date(2017, 3, 16, 9, 30, 0, 0, time.UTC),
		ModDate:      time.Date(2018, 7, 2, 14, 5, 0, 0, time.UTC),
	}

	nodes, _ := mustEmit(t, meta, nil, runInstant)
	root := documentRootOf(t, nodes)

	stamp := root.Metadata["collected_at"]
	require.NotEmpty(t, stamp, "the document root must record when the collect ran")

	parsed, err := time.Parse(time.RFC3339, stamp)
	require.NoError(t, err, "collected_at must be RFC3339; the listing parses it")
	require.True(t, parsed.Equal(runInstant),
		"the stamp must be the run instant, got %v want %v", parsed.UTC(), runInstant)

	// KNOWN POSITIVE: the PDF's own dates are still recorded, and neither is
	// what collected_at carries.
	creation := root.Metadata["creation_date"]
	mod := root.Metadata["mod_date"]
	require.NotEmpty(t, creation, "creation_date must still be recorded; without it the next assertion is vacuous")
	require.NotEmpty(t, mod, "mod_date must still be recorded; without it the next assertion is vacuous")
	require.NotEqual(t, creation, stamp, "collected_at must not alias the PDF's own creation_date")
	require.NotEqual(t, mod, stamp, "collected_at must not alias the PDF's own mod_date")

	// A later collect records a later instant — a constant would not move.
	later := runInstant.Add(90 * time.Minute)
	nodesLater, _ := mustEmit(t, meta, nil, later)
	stampLater := documentRootOf(t, nodesLater).Metadata["collected_at"]
	require.NotEqual(t, stamp, stampLater, "a later collect must record a later instant, not a constant")

	// A zero instant writes NO key, so a graph collected before this stamp
	// existed renders as unstamped rather than as the zero time.
	nodesUnstamped, _ := mustEmit(t, meta, nil, time.Time{})
	_, present := documentRootOf(t, nodesUnstamped).Metadata["collected_at"]
	require.False(t, present, "a zero collect instant must write no key at all; absence stays absent")
}
