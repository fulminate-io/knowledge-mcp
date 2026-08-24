// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mapMergeReference is the MAP-SHAPED merge the streamed writer replaced, kept
// verbatim as a test-only oracle. It inverts each input into a per-document term
// list, splices the survivors into Go maps, and encodes the finished accumulator
// — which is exactly the shape whose peak heap this ticket exists to remove.
//
// It lives here, not in the package, because that is now its only job: a
// reference the streamed merge is proven identical to. Keeping a second merge in
// production would be dead code; deleting it outright would leave the streamed
// merge with nothing independent to be checked against.
func mapMergeReference(
	t *testing.T, ins []*mappedSegment, accept []func(searchengine.ExternalID) bool,
) *mappedSegment {
	t.Helper()
	configs := defaultFieldConfigs
	out := make([]*fieldData, len(configs))
	byName := make(map[string]*fieldData, len(configs))
	for i, cfg := range configs {
		fd := &fieldData{config: cfg, postings: make(map[string][]posting)}
		out[i] = fd
		byName[cfg.Name] = fd
	}
	var members []searchengine.ExternalID
	docFreq := make(map[string]int64)
	winner := resolveMergeWinners(ins, accept)

	for i, ms := range ins {
		refMergeOne(ms, i, acceptFor(accept, i), winner, out, byName, &members, docFreq)
	}
	acc := &bm25Segment{fields: out, fieldByName: byName, members: members, docFreq: docFreq}
	blob, err := encodeSegmentV2(acc, defaultDictKind)
	require.NoError(t, err)
	seg, err := openSegmentV2(blob)
	require.NoError(t, err)
	return seg
}

// refMergeOne splices one input's live members into the reference output,
// re-numbering its segment-local docIDs into the consolidated space.
func refMergeOne(
	ms *mappedSegment, segIdx int, keep func(searchengine.ExternalID) bool,
	winner map[searchengine.ExternalID]mergeSlot,
	out []*fieldData, byName map[string]*fieldData,
	members *[]searchengine.ExternalID, docFreq map[string]int64,
) {
	type tref struct {
		field string
		term  string
		tf    uint16
	}
	perDoc := make([][]tref, ms.docCount)
	for _, mf := range ms.fields {
		mf.eachTerm(func(term string, docIDs []uint32, tfs []uint16) {
			held := strings.Clone(term)
			for i, docID := range docIDs {
				if int(docID) < len(perDoc) {
					perDoc[docID] = append(perDoc[docID], tref{field: mf.config.Name, term: held, tf: tfs[i]})
				}
			}
		})
	}
	for oldID := range ms.docCount {
		extID := strings.Clone(ms.member(oldID))
		if keep != nil && !keep(extID) {
			continue
		}
		if !winsSlot(winner, extID, segIdx, oldID) {
			continue
		}
		newID := uint32(len(*members))
		*members = append(*members, extID)
		for _, fd := range out {
			fd.docLengths = append(fd.docLengths, 0)
		}
		uniqueTerms := make(map[string]struct{})
		for _, mf := range ms.fields {
			if oldID < len(mf.lengths) {
				dl := int(mf.lengths[oldID])
				byName[mf.config.Name].docLengths[newID] = dl
				byName[mf.config.Name].totalTokens += int64(dl)
			}
		}
		for _, ref := range perDoc[oldID] {
			fd := byName[ref.field]
			if fd == nil {
				continue
			}
			fd.postings[ref.term] = append(fd.postings[ref.term], posting{docID: newID, tf: ref.tf})
			uniqueTerms[ref.term] = struct{}{}
		}
		for term := range uniqueTerms {
			docFreq[term]++
		}
	}
}
