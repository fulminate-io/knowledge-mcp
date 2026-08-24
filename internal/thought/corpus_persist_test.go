// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt"
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

// corpusPlaintextSentinel is 95 printable characters, planted in a node field that
// reaches the serialized frame. The length is chosen so a literal `strings -n 60`
// sweep over the on-disk record would report it — which is what makes the assertion
// below no weaker than the shell probe an operator would run by hand.
const corpusPlaintextSentinel = "SENTINEL-a-recognizable-run-of-printable-characters-that-any-strings-sweep-would-surface-000000"

// TestCorpusRecord_OnDiskIsCiphertext is the confidentiality claim expressed in Go
// so it runs in CI rather than only under an operator's shell.
//
// Leg (a) asserts an ABSENCE, so leg (b) supplies the known-positive control in the
// SAME run: the identical sentinel IS a substring of the unsealed frame. Without
// that control a fixture which never carried the sentinel — or a field the
// marshaller drops — would satisfy leg (a) exactly as well as a working seal.
// Leg (c) then proves the file is still readable by its owner, so "unreadable" is
// not how (a) was achieved.
func TestCorpusRecord_OnDiskIsCiphertext(t *testing.T) {
	root := t.TempDir()
	path := CorpusCachePathFor(root)

	items, cursors := corpusRecordFixture()
	items[0].SymbolName = corpusPlaintextSentinel

	// (b) KNOWN-POSITIVE CONTROL: the sentinel survives into the unsealed frame.
	unsealed, err := encodeCorpusRecord(corpusNodeTypes, items, cursors)
	require.NoError(t, err)
	require.True(t, bytes.Contains(unsealed, []byte(corpusPlaintextSentinel)),
		"control: the sentinel must reach the serialized frame, or the absence check below proves nothing")

	// (a) The record committed to disk is an envelope carrying no readable trace.
	require.NoError(t, saveCorpusRecord(path, corpusNodeTypes, items, cursors))
	onDisk, rerr := os.ReadFile(path) //nolint:gosec // path is CorpusCachePathFor(t.TempDir()).
	require.NoError(t, rerr)
	require.GreaterOrEqual(t, len(onDisk), 4)
	assert.Equal(t, []byte("KCE1"), onDisk[:4], "the record on disk carries the sealed envelope magic")
	assert.NotContains(t, string(onDisk), corpusCacheMagic,
		"the inner frame magic must not be readable on disk either")
	assert.False(t, bytes.Contains(onDisk, []byte(corpusPlaintextSentinel)),
		"the on-disk record contains readable node content")

	// (c) Still readable by its owner.
	got, ok, lerr := loadCorpusRecord(path, corpusNodeTypes)
	require.NoError(t, lerr)
	require.True(t, ok)
	require.Len(t, got.GetItems(), len(items))
	assert.Equal(t, corpusPlaintextSentinel, got.GetItems()[0].GetSymbolName(),
		"the sealed record round-trips to the same content it was built from")
}

// TestCorpusRecord_LegacyPlaintextDroppedAndRebuilt covers the migration posture:
// a record written before this cache was encrypted is DROPPED and rebuilt, never
// converted.
//
// The legacy fixture goes through encodeCorpusRecord rather than a hand-written
// byte string, because only the real encoder reproduces the exact pre-change
// on-disk shape — a look-alike would test the assertion rather than the format.
//
// The FILE-GONE leg is the load-bearing one. It drives the LOOP, not the codec,
// and it is what fails if the removal at rejection were missing; the error-text
// leg alone passes with 53 MB of readable plaintext still sitting on disk.
func TestCorpusRecord_LegacyPlaintextDroppedAndRebuilt(t *testing.T) {
	dir := t.TempDir()
	path := CorpusCachePathFor(dir)

	plantLegacy := func(t *testing.T) {
		t.Helper()
		items, cursors := corpusRecordFixture()
		legacy, err := encodeCorpusRecord(corpusNodeTypes, items, cursors)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, legacy, 0o600)) //nolint:gosec // path is CorpusCachePathFor(t.TempDir()).
	}

	// The codec half: a legacy record is diagnosed as legacy, not as damage.
	plantLegacy(t)
	got, ok, err := loadCorpusRecord(path, corpusNodeTypes)
	require.Error(t, err, "a legacy plaintext record must be refused")
	assert.Nil(t, got)
	assert.False(t, ok)
	require.ErrorIs(t, err, filecrypt.ErrLegacyPlaintext)
	assert.Contains(t, err.Error(), "legacy plaintext record",
		"the operator-facing message must name the condition")

	// The loop half: the rejected file is GONE afterwards, and the loop cold-drains.
	require.FileExists(t, path, "control: the planted legacy record exists before the loop runs")
	rows := []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}, {"t3", 3000, false}}
	fake := &fakeCorpusScanner{rows: rows, freshH: 10_000_000}
	p := warmLoop(fake, dir)
	p.refreshCorpusCache(context.Background())

	require.NotEmpty(t, fake.cursorsSeen)
	assert.Equal(t, int64(0), fake.cursorsSeen[0],
		"a rejected legacy record must leave the cache empty, so the drain starts from a ZERO cursor")

	// Rebuilt, not converted: what is on disk now is a sealed record, not the frame
	// that was planted.
	rebuilt, rerr := os.ReadFile(path) //nolint:gosec // path is CorpusCachePathFor(t.TempDir()).
	require.NoError(t, rerr)
	require.GreaterOrEqual(t, len(rebuilt), 4)
	assert.Equal(t, []byte("KCE1"), rebuilt[:4], "the record was rebuilt sealed")
}

// TestCorpusRecord_LegacyRecordRemovedAtRejection isolates the removal itself, with
// the drain disabled so nothing can rewrite the file behind the assertion. Without
// this the file-gone check above could be satisfied by the rebuild alone.
func TestCorpusRecord_LegacyRecordRemovedAtRejection(t *testing.T) {
	dir := t.TempDir()
	path := CorpusCachePathFor(dir)

	items, cursors := corpusRecordFixture()
	legacy, err := encodeCorpusRecord(corpusNodeTypes, items, cursors)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, legacy, 0o600)) //nolint:gosec // path is CorpusCachePathFor(t.TempDir()).
	require.FileExists(t, path, "control: the planted record exists before the load")

	p := (&PropagationLoop{}).WithCorpusPersistence(dir)
	p.corpus = newCorpusCache()
	adopted := p.warmLoadCorpusOnce()

	assert.False(t, adopted, "a legacy record must not be adopted")
	assert.NoFileExists(t, path,
		"the rejected record must be removed at rejection, not left on disk until some later persist")
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
