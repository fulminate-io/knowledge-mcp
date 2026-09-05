// SPDX-License-Identifier: Apache-2.0

package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// collectStampPage builds the smallest pageRecord that still produces a page
// root, with a caller-chosen FetchedAt so the test can hold the per-page fetch
// instant and the run-wide collect instant apart.
func collectStampPage(url string, fetchedAt time.Time) *pageRecord {
	return &pageRecord{
		URL:         url,
		FinalURL:    url,
		Title:       "T",
		HTTPStatus:  200,
		FetchedAt:   fetchedAt,
		ContentHash: "hash-" + url,
		TopSections: []*sectionRecord{{
			Heading:  "Intro",
			Depth:    1,
			Children: []contentRecord{paragraphRecord{Text: "body"}},
		}},
	}
}

// pageRootOf returns the single "page" node from an emitted node slice.
func pageRootOf(t *testing.T, nodes []*knowledgev1.Node) *knowledgev1.Node {
	t.Helper()
	var found *knowledgev1.Node
	for _, n := range nodes {
		if n.Type == "page" {
			require.Nil(t, found, "more than one page root emitted; the assertions below would read an arbitrary one")
			found = n
		}
	}
	require.NotNil(t, found, "no page root emitted; every assertion below would be vacuous")
	return found
}

// TestEmitFromPage_CollectStampIsPerRunNotPerPage fences collected_at as an
// identifier OF THE COLLECT RUN rather than of the page.
//
// A crawl of N pages emits N page roots. If the stamp were derived per page,
// one graph would disagree with itself about when it was collected, and the
// modules listing built on top of it could not name a single collect instant
// for the graph. The three wrong-but-compiling implementations this separates
// are: plumbing the parameter through and never writing it, aliasing the
// page's own FetchedAt, and hardcoding a constant.
//
// KNOWN POSITIVE ON THE SAME READ PATH: fetched_at is asserted PRESENT on both
// roots and DIFFERENT between them, through the same metadata map the stamp is
// read from. Without that leg, "collected_at is not fetched_at" would be
// satisfied by a metadata map that lost both keys.
func TestEmitFromPage_CollectStampIsPerRunNotPerPage(t *testing.T) {
	t.Parallel()

	runInstant := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fetchA := time.Date(2026, 9, 1, 11, 58, 3, 0, time.UTC)
	fetchB := time.Date(2026, 9, 1, 11, 59, 41, 0, time.UTC)

	nodesA, _ := mustEmitFromPage(t, collectStampPage("https://example.com/a", fetchA), runInstant)
	nodesB, _ := mustEmitFromPage(t, collectStampPage("https://example.com/b", fetchB), runInstant)

	rootA := pageRootOf(t, nodesA)
	rootB := pageRootOf(t, nodesB)

	stampA := rootA.Metadata["collected_at"]
	stampB := rootB.Metadata["collected_at"]
	require.NotEmpty(t, stampA, "the page root must record when the collect ran")
	require.NotEmpty(t, stampB, "the page root must record when the collect ran")

	// One collect, one instant — on every page root it produced.
	require.Equal(t, stampA, stampB, "both pages of one collect must record the SAME collect instant")

	// The stamp is a real RFC3339 instant, and it is the run instant.
	parsed, err := time.Parse(time.RFC3339, stampA)
	require.NoError(t, err, "collected_at must be RFC3339; the listing parses it")
	require.True(t, parsed.Equal(runInstant),
		"the stamp must be the run instant, got %v want %v", parsed.UTC(), runInstant)

	// KNOWN POSITIVE: the per-page fetch instant is still recorded, still
	// per-page, and is not what collected_at carries.
	fetchedA := rootA.Metadata["fetched_at"]
	fetchedB := rootB.Metadata["fetched_at"]
	require.NotEmpty(t, fetchedA, "fetched_at must still be recorded; without it the next assertion is vacuous")
	require.NotEmpty(t, fetchedB, "fetched_at must still be recorded; without it the next assertion is vacuous")
	require.NotEqual(t, fetchedA, fetchedB, "the two fixture pages were fetched at different instants; fetched_at must still differ")
	require.NotEqual(t, fetchedA, stampA, "collected_at must not alias the page's own fetched_at")

	// A later collect records a later instant — a constant would not move.
	later := runInstant.Add(90 * time.Minute)
	nodesLater, _ := mustEmitFromPage(t, collectStampPage("https://example.com/a", fetchA), later)
	stampLater := pageRootOf(t, nodesLater).Metadata["collected_at"]
	require.NotEqual(t, stampA, stampLater, "a later collect must record a later instant, not a constant")

	// A zero instant writes NO key, so a graph collected before this stamp
	// existed renders as unstamped rather than as the zero time.
	nodesUnstamped, _ := mustEmitFromPage(t, collectStampPage("https://example.com/a", fetchA), time.Time{})
	_, present := pageRootOf(t, nodesUnstamped).Metadata["collected_at"]
	require.False(t, present, "a zero collect instant must write no key at all; absence stays absent")
}
