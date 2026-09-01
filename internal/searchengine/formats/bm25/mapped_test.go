// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// dictKinds is every encoding a blob may declare. Each is exercised by the
// equality proof, because the dictionary is the one part of the read path that
// differs between them and a bug in any one is a wrong search result.
var dictKinds = []struct {
	name string
	kind byte
}{
	{"flat", dictFlat},
	{"blocked", dictBlocked},
	{"hash", dictHash},
}

// equalityQueries are multi-term queries over the manyTermDocs vocabulary. They
// are deliberately long so each one matches a large share of the corpus: an
// equality test that compares two empty result sets passes while proving
// nothing, so every query here is asserted to return hits.
var equalityQueries = []string{
	"term0007 term0031 term0064 term0097 term0128 term0155 term0193 term0221",
	"term0011 term0042 term0073 term0106 term0139 term0171 term0204 term0233",
	"term0019 term0055 term0088 term0119 term0147 term0182 term0212 term0245",
	"term0402 term0417 term0433 term0448 term0461 term0479 term0492 term0507",
	"term0256 term0288 term0317 term0349 term0366 term0374 term0523 term0588",
}

// openKind encodes the accumulator in one dictionary encoding and opens it.
func openKind(t *testing.T, acc *bm25Segment, kind byte) *mappedSegment {
	t.Helper()
	blob, err := encodeSegmentV2(acc, kind)
	require.NoError(t, err)
	seg, err := openSegmentV2(blob)
	require.NoError(t, err)
	return seg
}

// TestOffsetReaderMatchesMapReader is the equality proof the whole conversion
// rests on: for every dictionary encoding, the offset-addressed reader returns
// the SAME hits, in the same order, with bit-identical scores as the map-shaped
// reader it replaces.
//
// Two vacuity guards, because an equality assertion is the easiest kind of test
// to make pass by measuring nothing. Every query is required to return hits, so
// the comparison is never empty-against-empty; and the two readers are scored
// under INDEPENDENTLY built corpus statistics, which are themselves compared
// term by term first — sharing one stats object would hide a defect in the
// mapped document-frequency dictionary.
func TestOffsetReaderMatchesMapReader(t *testing.T) {
	docs := manyTermDocs(400)
	acc := buildAccumulator(t, docs)
	terms := sortedKeys(acc.docFreq)
	require.NotEmpty(t, terms)

	mapStats := newCorpusStats()
	mapStats.TotalDocs = int64(acc.docCount())
	mapStats.attach(acc)
	for _, fd := range acc.fields {
		mapStats.FieldAvgLen[fd.config.Name] = float64(fd.totalTokens) / float64(acc.docCount())
	}

	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			seg := openKind(t, acc, dk.kind)
			offStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{seg})
			require.Equal(t, mapStats.TotalDocs, offStats.TotalDocs)
			require.Equal(t, docFreqSnapshot(mapStats, terms), docFreqSnapshot(offStats, terms),
				"the mapped docFreq dictionary must resolve to the same values the map form holds")

			for qi, text := range equalityQueries {
				q := NewQuery(text)
				want := acc.Search(q, mapStats, 25, nil)
				got := seg.Search(q, offStats, 25, nil)
				require.NotEmpty(t, want,
					"query %d matched nothing — an equality assertion over two empty sets proves nothing", qi)
				require.Len(t, got, len(want), "query %d hit count", qi)
				for i := range want {
					require.Equal(t, want[i].ID, got[i].ID, "query %d rank %d id", qi, i)
					require.Equal(t, math.Float64bits(want[i].Score), math.Float64bits(got[i].Score),
						"query %d rank %d: offset reader scored %v, map reader scored %v",
						qi, i, got[i].Score, want[i].Score)
				}
			}

			// Member sets must agree too, not just the scored top-k.
			require.Equal(t, acc.IDs(), seg.IDs())
		})
	}
}

// TestOpenRejectsMalformedBlob proves a blob that violates the layout is refused
// outright rather than partially read. Each case mutates ONE field of a copy of a
// known-good blob, so the control — that the unmutated blob opens cleanly — is
// established in the same run and a rejection cannot be an artifact of the
// fixture being unopenable to begin with.
func TestOpenRejectsMalformedBlob(t *testing.T) {
	acc := buildAccumulator(t, sampleDocs())
	good, err := encodeSegmentV2(acc, defaultDictKind)
	require.NoError(t, err)

	// Known-positive: the unmutated blob opens.
	_, err = openSegmentV2(good)
	require.NoError(t, err, "the fixture blob must open, or every rejection below is meaningless")

	cases := []struct {
		name    string
		mutate  func(b []byte)
		wantMsg string
	}{{
		name:    "wrong version",
		mutate:  func(b []byte) { b[v2HdrVersion] = 1 },
		wantMsg: "unsupported serial version 1",
	}, {
		name:    "blobLen mismatch",
		mutate:  func(b []byte) { binary.LittleEndian.PutUint32(b[v2HdrBlobLen:], uint32(len(b)-1)) },
		wantMsg: "header declares",
	}, {
		name: "misaligned section",
		mutate: func(b []byte) {
			off := binary.LittleEndian.Uint32(b[v2HdrMemberOffsets:])
			binary.LittleEndian.PutUint32(b[v2HdrMemberOffsets:], off+1)
		},
		wantMsg: "is not 4-aligned",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := slices.Clone(good)
			tc.mutate(bad)
			seg, err := openSegmentV2(bad)
			require.Error(t, err, "a %s blob must be refused", tc.name)
			require.Nil(t, seg, "a refused blob must not yield a partially read segment")
			require.Contains(t, err.Error(), tc.wantMsg)
		})
	}

	// The version error must also carry the remedy, since reject-and-rebuild IS
	// the migration and an operator reading the log needs to know that.
	bad := slices.Clone(good)
	bad[v2HdrVersion] = 1
	_, err = openSegmentV2(bad)
	require.ErrorContains(t, err, "rebuild")
}

// TestCorruptTermOffsetIsRefusedNotRead extends the malformed-blob family to the
// one class open's section checks CANNOT cover: a row whose stored term offset
// and length point outside the blob.
//
// checkSection validates where a SECTION lives; a term offset is data stored
// INSIDE a row, and validating every row at open would be the eager per-row walk
// lazy open exists to avoid. So the guard is at the accessor, and the blob is
// refused there — loudly, naming the span — rather than read.
//
// That a read WOULD otherwise happen is not assumed: Go bounds-checks the
// indexing expression, so termOff alone cannot escape, but unsafe.String then
// takes termLen bytes onward unchecked. The mutation below leaves termOff in
// range and blows only the length, which is precisely the shape the index check
// does not see. Before the guard, open ACCEPTED this blob.
func TestCorruptTermOffsetIsRefusedNotRead(t *testing.T) {
	acc := buildAccumulator(t, sampleDocs())

	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			good, err := encodeSegmentV2(acc, dk.kind)
			require.NoError(t, err)

			// KNOWN-POSITIVE: unmutated, the same probes read clean.
			clean, err := openSegmentV2(good)
			require.NoError(t, err)
			require.NotZero(t, clean.segmentDocFreq("cluster"),
				"the fixture term is missing, so the refusal below would prove nothing")

			bad := slices.Clone(good)
			off := int(binary.LittleEndian.Uint32(bad[v2HdrDocFreqDict:]))
			rows := int(binary.LittleEndian.Uint32(bad[off+4:]))
			// Row 0 keeps a valid termOff and declares a length far past the end.
			binary.LittleEndian.PutUint32(bad[rows+4:], uint32(len(bad)*4))

			seg, err := openSegmentV2(bad)
			require.NoError(t, err, "open is lazy by design and does not walk rows")
			// THE REFUSAL IS NOW A TYPED VALUE, NOT A STRING, and the type is the
			// load-bearing half. The message is unchanged, but it is carried by a
			// *searchengine.CorruptSegmentError so the engine's per-segment
			// boundary can contain it and quarantine the file instead of the
			// process dying — a bare string panic is indistinguishable from a
			// logic bug and cannot be recovered without swallowing real defects.
			wantDetail := fmt.Sprintf("bm25: docFreq term spans [%d,%d) in a %d-byte blob",
				int(binary.LittleEndian.Uint32(bad[rows:])),
				int(binary.LittleEndian.Uint32(bad[rows:]))+len(bad)*4, len(bad))
			raised := func() any {
				var r any
				func() {
					defer func() { r = recover() }()
					seg.docFreqEach(func(string, int64) {})
				}()
				return r
			}()
			require.NotNil(t, raised, "a row pointing outside the blob must be refused loudly, not read")
			ce, ok := raised.(*searchengine.CorruptSegmentError)
			require.Truef(t, ok, "refusal must be a *searchengine.CorruptSegmentError, got %T", raised)
			require.Equal(t, wantDetail, ce.Detail)
		})
	}
}

// TestHitIDsDoNotAliasBlob proves no string crossing the segment's API boundary
// points into the blob. Every id the segment returns outlives the caller's
// interest in the bytes it was read from, so a view would become a read of
// released memory the moment the blob is a mapping rather than a heap slice.
//
// The control is the internal accessor: seg.member is REQUIRED to alias, which
// is what proves the address-range test can detect aliasing at all. Without it a
// mistake in the range arithmetic would report "nothing aliases" for everything.
func TestHitIDsDoNotAliasBlob(t *testing.T) {
	docs := manyTermDocs(200)
	acc := buildAccumulator(t, docs)
	seg := openKind(t, acc, defaultDictKind)
	stats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{seg})

	//nolint:gosec // address arithmetic is the point: the test asks WHERE a string's bytes live
	base := uintptr(unsafe.Pointer(unsafe.SliceData(seg.blob)))
	end := base + uintptr(len(seg.blob))
	aliasesBlob := func(s string) bool {
		if len(s) == 0 {
			return false
		}
		//nolint:gosec // address arithmetic is the point: the test asks WHERE a string's bytes live
		p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
		return p >= base && p < end
	}

	// Known-positive: the INTERNAL member view does alias, so the detector works.
	require.True(t, aliasesBlob(seg.member(0)),
		"the internal member view must alias the blob, or this test cannot detect aliasing")

	ids := seg.IDs()
	require.Len(t, ids, seg.docCount)
	for i, id := range ids {
		require.False(t, aliasesBlob(id), "IDs()[%d] = %q aliases the blob", i, id)
	}

	hits := seg.Search(NewQuery(equalityQueries[0]), stats, 25, nil)
	require.NotEmpty(t, hits, "no hits — an aliasing check over zero returned ids proves nothing")
	for i, h := range hits {
		require.False(t, aliasesBlob(h.ID), "hit %d ID %q aliases the blob", i, h.ID)
	}
}

// TestAggregateStatsHeaderOnlyLazyDocFreq proves corpus statistics are folded
// from segment HEADERS and that document frequency is resolved on demand to the
// same values the eager fold produced.
//
// The before/after on the memo is what catches a silent revert to the eager map:
// an implementation that summed every segment's dictionary at aggregation time
// would have to have those frequencies resolved by the time AggregateStats
// returns, and the memo is now the only place a corpus-global frequency lives.
func TestAggregateStatsHeaderOnlyLazyDocFreq(t *testing.T) {
	docs := manyTermDocs(300)
	acc := buildAccumulator(t, docs)
	seg := buildBM25(t, docs)

	stats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{seg})
	require.Empty(t, stats.memo,
		"AggregateStats must resolve NO document frequency — a populated memo means the eager fold is back")

	// It did read the headers: doc count and per-field token totals are correct.
	require.Equal(t, int64(acc.docCount()), stats.TotalDocs)
	require.NotEmpty(t, stats.FieldAvgLen)
	for _, fd := range acc.fields {
		require.InDelta(t, float64(fd.totalTokens)/float64(acc.docCount()),
			stats.FieldAvgLen[fd.config.Name], 1e-12, "field %s average length", fd.config.Name)
	}

	terms := sortedKeys(acc.docFreq)
	require.NotEmpty(t, terms)
	nonZero := 0
	for _, term := range terms {
		got := stats.docFreqOf(term)
		require.Equal(t, acc.docFreq[term], got, "term %q document frequency", term)
		if got > 0 {
			nonZero++
		}
	}
	require.Equal(t, len(terms), nonZero,
		"every indexed term must resolve to a non-zero frequency; an all-zero result would make the comparison above vacuous")
	require.Len(t, stats.memo, len(terms), "resolved frequencies must be memoized")

	// A term the corpus does not hold resolves to zero — the same answer the
	// eager map gave by having no entry for it.
	require.Zero(t, stats.docFreqOf("no-such-term-in-this-corpus"))
}

// TestDocFreqOfSumsAcrossSegments pins that a lazily resolved frequency is the
// CORPUS-global sum, not one segment's count. A single-segment fixture cannot
// tell the two apart, which is what this splits the corpus to catch.
func TestDocFreqOfSumsAcrossSegments(t *testing.T) {
	docs := manyTermDocs(200)
	half := len(docs) / 2
	segA, err := Format{}.Build(docs[:half])
	require.NoError(t, err)
	segB, err := Format{}.Build(docs[half:])
	require.NoError(t, err)

	whole := buildAccumulator(t, docs)
	stats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{segA, segB})

	split := 0
	for _, term := range sortedKeys(whole.docFreq) {
		want := whole.docFreq[term]
		require.Equal(t, want, stats.docFreqOf(term), "term %q", term)
		a := segA.(*mappedSegment).segmentDocFreq(term)
		if a > 0 && a < want {
			split++
		}
	}
	require.Positive(t, split,
		"no term spans both segments, so this fixture could not tell a per-segment count from a corpus sum")
}

// TestDocFreqEachWalksAscending pins the contract the streamed merge is built
// on: docFreqEach enumerates EVERY document frequency the segment holds, in
// ascending term order. A k-way union over several segments' dictionaries is
// only correct if each one arrives sorted, and a walk that silently skipped
// terms would produce a merged segment with frequencies that are quietly low.
func TestDocFreqEachWalksAscending(t *testing.T) {
	docs := manyTermDocs(200)
	acc := buildAccumulator(t, docs)

	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			seg := openKind(t, acc, dk.kind)
			got := make(map[string]int64)
			var order []string
			seg.docFreqEach(func(term string, df int64) {
				order = append(order, term)
				got[term] = df
			})
			require.NotEmpty(t, order, "an empty walk would make the comparisons below vacuous")
			require.True(t, slices.IsSorted(order), "docFreqEach must enumerate in ascending term order")
			require.Equal(t, acc.docFreq, got, "docFreqEach must enumerate every frequency the segment holds")
		})
	}
}
