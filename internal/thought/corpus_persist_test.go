// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// corpusRecordFixture builds the node set + cursors every test in this file frames.
// One node carries metadata (the field most likely to be dropped by a hand-rolled
// codec) and the two cursors carry DIFFERENT layer keys, so a codec that collapsed
// the per-layer map to a single entry would fail the round-trip.
func corpusRecordFixture() ([]*knowledgev1.Node, []*knowledgev1.LayerCursor) {
	items := []*knowledgev1.Node{
		{
			Id:         "t-1",
			Type:       "thought",
			SymbolName: "a thought with metadata",
			UpdatedAt:  1000,
			Metadata:   map[string]string{"origin": "implementer", "cluster_id": "c-9"},
		},
		{Id: "c-1", Type: "charge", SymbolName: "a charge", UpdatedAt: 1100},
		{Id: "s-1", Type: "thought_session", SymbolName: "a session", UpdatedAt: 1200},
	}
	cursors := []*knowledgev1.LayerCursor{
		{LayerKey: "thought", AfterUpdatedAt: 1200, AfterId: "s-1"},
		{LayerKey: "charge", AfterUpdatedAt: 1100, AfterId: "c-1"},
	}
	return items, cursors
}

func TestCorpusRecord_RoundTrip(t *testing.T) {
	items, cursors := corpusRecordFixture()

	raw, err := encodeCorpusRecord(corpusNodeTypes, items, cursors)
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	got, err := decodeCorpusRecord(raw, corpusNodeTypes)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Items compared by identity + the ordering key the cache merges on, plus the
	// metadata map, rather than by pointer.
	require.Len(t, got.GetItems(), len(items))
	for i, want := range items {
		gotItem := got.GetItems()[i]
		assert.Equal(t, want.GetId(), gotItem.GetId())
		assert.Equal(t, want.GetType(), gotItem.GetType())
		assert.Equal(t, want.GetUpdatedAt(), gotItem.GetUpdatedAt())
		assert.Equal(t, want.GetMetadata(), gotItem.GetMetadata())
	}

	require.Len(t, got.GetNextCursors(), len(cursors))
	byKey := map[string]*knowledgev1.LayerCursor{}
	for _, c := range got.GetNextCursors() {
		byKey[c.GetLayerKey()] = c
	}
	for _, want := range cursors {
		gotCur, ok := byKey[want.GetLayerKey()]
		require.True(t, ok, "layer %q survived the round-trip", want.GetLayerKey())
		assert.Equal(t, want.GetAfterUpdatedAt(), gotCur.GetAfterUpdatedAt())
		assert.Equal(t, want.GetAfterId(), gotCur.GetAfterId())
	}

	// KNOWN-POSITIVE CONTROL for the reject test below: this same fixture decodes
	// clean, so a rejection there is the damage being caught and not the decoder
	// refusing everything it is handed.
	assert.NotEmpty(t, got.GetItems())
}

func TestCorpusRecord_RejectsTamperedOrTruncated(t *testing.T) {
	items, cursors := corpusRecordFixture()
	pristine, err := encodeCorpusRecord(corpusNodeTypes, items, cursors)
	require.NoError(t, err)

	// The control: the untampered record decodes. Every case below mutates a COPY of
	// exactly this buffer, so a non-nil error is attributable to the mutation.
	control, err := decodeCorpusRecord(pristine, corpusNodeTypes)
	require.NoError(t, err, "control: the pristine record must decode")
	require.Len(t, control.GetItems(), len(items))

	tamper := func(mutate func(b []byte) []byte) []byte {
		cp := make([]byte, len(pristine))
		copy(cp, pristine)
		return mutate(cp)
	}

	// The payload region begins after magic + version + typesLen + typesBytes +
	// payloadLen + checksum.
	payloadStart := corpusFrameFixedHead + len(strings.Join(corpusNodeTypes, ",")) + 4 + corpusFrameChecksum
	require.Less(t, payloadStart, len(pristine), "fixture must carry a non-empty payload")

	cases := []struct {
		name      string
		raw       []byte
		wantTypes []string
	}{
		{
			name:      "bad magic",
			raw:       tamper(func(b []byte) []byte { b[0] = 'X'; return b }),
			wantTypes: corpusNodeTypes,
		},
		{
			name:      "version bumped to 2",
			raw:       tamper(func(b []byte) []byte { b[corpusFrameMagicLen+3] = 2; return b }),
			wantTypes: corpusNodeTypes,
		},
		{
			name:      "truncated inside the payload",
			raw:       tamper(func(b []byte) []byte { return b[:len(b)-8] }),
			wantTypes: corpusNodeTypes,
		},
		{
			name:      "one payload byte flipped",
			raw:       tamper(func(b []byte) []byte { b[payloadStart] ^= 0xFF; return b }),
			wantTypes: corpusNodeTypes,
		},
		{
			name:      "node-type set changed",
			raw:       pristine,
			wantTypes: append(append([]string{}, corpusNodeTypes...), "document"),
		},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, derr := decodeCorpusRecord(tc.raw, tc.wantTypes)
			require.Error(t, derr, "the damaged record must be rejected")
			assert.Nil(t, got, "a rejected record must yield NO partial cache")
			seen[tc.name] = derr.Error()
		})
	}

	// Distinguishable messages: an operator must be able to tell corruption from a
	// node-type-set change. Five cases, five distinct error strings.
	require.Len(t, seen, len(cases))
	distinct := map[string]string{}
	for name, msg := range seen {
		prior, dup := distinct[msg]
		require.False(t, dup, "case %q and case %q report the same error %q", name, prior, msg)
		distinct[msg] = name
	}
	assert.Len(t, distinct, len(cases))
}

// TestCorpusRecord_SaveLoadOnDisk exercises the three disk-facing helpers the
// codec tests above cannot reach: the save path, the atomic write beneath it, and
// the absent-record rule. Absence returns (nil, false, nil) — the ordinary
// first-run / wiped-cache state, which the loop must be able to tell apart from a
// damaged record, because the two carry different log levels and the same
// disposition only by coincidence.
func TestCorpusRecord_SaveLoadOnDisk(t *testing.T) {
	root := t.TempDir()
	path := CorpusCachePathFor(root)

	// Absent: not an error, not a record.
	got, ok, err := loadCorpusRecord(path, corpusNodeTypes)
	require.NoError(t, err, "an absent record is the ordinary first-run state, not an error")
	assert.False(t, ok)
	assert.Nil(t, got)

	items, cursors := corpusRecordFixture()
	require.NoError(t, saveCorpusRecord(path, corpusNodeTypes, items, cursors))

	got, ok, err = loadCorpusRecord(path, corpusNodeTypes)
	require.NoError(t, err)
	require.True(t, ok, "the record just written must load")
	require.NotNil(t, got)
	assert.Len(t, got.GetItems(), len(items))
	assert.Len(t, got.GetNextCursors(), len(cursors))

	// The atomic write leaves the record and nothing else — no *.tmp straggler from
	// the temp file the rename consumed.
	ents, rerr := os.ReadDir(filepath.Dir(path))
	require.NoError(t, rerr)
	require.Len(t, ents, 1, "exactly one file in the record dir — no temp leftovers")
	assert.Equal(t, corpusCacheFile, ents[0].Name())

	// Overwriting in place keeps the single-entry invariant.
	require.NoError(t, saveCorpusRecord(path, corpusNodeTypes, items[:1], cursors[:1]))
	ents, rerr = os.ReadDir(filepath.Dir(path))
	require.NoError(t, rerr)
	require.Len(t, ents, 1)
	got, ok, err = loadCorpusRecord(path, corpusNodeTypes)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Len(t, got.GetItems(), 1, "the rewrite replaced the record wholesale")
}

func TestCorpusCachePath_UnderDataRootNotSegments(t *testing.T) {
	root := t.TempDir()

	assert.Equal(t,
		filepath.Join(root, "thought", "knowledge", "default", "corpus.bin"),
		CorpusCachePathFor(root))

	// The record must NOT live under the segment cache tree: DropGraphCache
	// enumerates every directory there as a storage format and removes the
	// per-graph directory beneath it, so a record parked inside would be swept by
	// any graph drop. This assertion is the one that fails if the record is later
	// "tidied" into the segment tree; the behavioral consequence is asserted in
	// the bootstrap non-interaction test.
	assert.False(t,
		strings.HasPrefix(CorpusCachePathFor(root), filepath.Join(root, "segments")),
		"the corpus record must not be rooted under the segment cache tree")
}
