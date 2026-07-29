// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// keysetCorpus builds an id-ascending corpus of n ids, zero-padded so string
// order and numeric order agree — the drain's cursor contract is defined over
// the backend's id ordering, so the fixture must not smuggle in a different one.
func keysetCorpus(n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, fmt.Sprintf("id-%04d", i))
	}
	return ids
}

// keysetPage is the scripted backend: it serves the corpus slice STRICTLY AFTER
// afterID, capped at pageSize. An empty cursor serves from the head, which is the
// page-1 contract the drain depends on.
func keysetPage(corpus []string, afterID string, pageSize int) []string {
	start := 0
	if afterID != "" {
		for i, id := range corpus {
			if id == afterID {
				start = i + 1
				break
			}
		}
	}
	end := min(start+pageSize, len(corpus))
	return corpus[start:end]
}

func TestDrainKeysetPages(t *testing.T) {
	const pageSize = 4
	// 10 = two full pages plus a SHORT third, so termination is exercised on a
	// short-but-non-empty page rather than only on the empty-page path.
	corpus := keysetCorpus(10)

	var cursors []string
	got, err := DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursors = append(cursors, afterID)
		page := keysetPage(corpus, afterID, pageSize)
		nodes := make([]*knowledgev1.Node, 0, len(page))
		for _, id := range page {
			nodes = append(nodes, &knowledgev1.Node{Id: id})
		}
		return nodes, nil
	}, pageSize)
	require.NoError(t, err)

	// The cursor SEQUENCE is the real assertion: a drain that ignored the cursor
	// and re-served page 1 forever would still produce the right set through the
	// seen-set while looping, so the returned set alone cannot pin the behavior.
	assert.Equal(t, []string{"", "id-0003", "id-0007"}, cursors,
		"page 1 must receive the EMPTY cursor and each later page the previous page's LAST id")
	assert.Len(t, cursors, 3, "drain must terminate on the first SHORT page")

	gotIDs := make([]string, 0, len(got))
	for _, n := range got {
		gotIDs = append(gotIDs, n.GetId())
	}
	assert.Equal(t, corpus, gotIDs, "the drain returns the whole corpus, in order, with no duplicates")
}

func TestDrainKeysetIDs(t *testing.T) {
	const pageSize = 4
	corpus := keysetCorpus(10)

	var cursors []string
	got, err := DrainKeysetIDs(func(afterID string) ([]string, error) {
		cursors = append(cursors, afterID)
		return keysetPage(corpus, afterID, pageSize), nil
	}, pageSize)
	require.NoError(t, err)

	assert.Equal(t, []string{"", "id-0003", "id-0007"}, cursors,
		"page 1 must receive the EMPTY cursor and each later page the previous page's LAST id")
	assert.Len(t, cursors, 3, "drain must terminate on the first SHORT page")
	assert.Equal(t, corpus, got, "the drain returns the whole corpus, in order, with no duplicates")
}

// TestDrainKeysetIDsDedupes pins the seen-set invariant guard on the ids twin: a
// backend that re-emits a row at or before the cursor is a real bug, absorbed
// here rather than propagated to the caller as a duplicate.
func TestDrainKeysetIDsDedupes(t *testing.T) {
	const pageSize = 2
	pages := [][]string{{"a", "b"}, {"b", "c"}, {"d"}}
	i := 0
	got, err := DrainKeysetIDs(func(string) ([]string, error) {
		p := pages[i]
		i++
		return p, nil
	}, pageSize)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c", "d"}, got)
}
