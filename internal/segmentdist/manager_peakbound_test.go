// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// loadBoundSource is an in-memory segmentSource that backs List + Fetch from a
// seeded blob set and instruments Fetch for the peak-memory-bound assertions:
//   - records the id count of every Fetch call (maxFetchIDs),
//   - optionally rejects any Fetch whose summed content bytes exceed byteCeiling
//     with connect.CodeResourceExhausted (the server byte-ceiling stand-in), which
//     drives fetchMisses' adaptive halving through the real load() path.
//
// Each seeded blob carries a real byte payload so the byte-ceiling simulation is
// faithful (the cap is about bytes, not ids).
type loadBoundSource struct {
	mu          sync.Mutex
	blobs       []*knowledgev1.SegmentBlobProto // generation-ordered
	byteCeiling int                             // 0 = never reject
	maxFetchIDs int                             // largest id count seen in a single Fetch
	fetchCalls  int
}

func (c *loadBoundSource) List(_ context.Context, sinceGen uint64) ([]searchengine.SegmentMeta, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var metas []searchengine.SegmentMeta
	for _, b := range c.blobs {
		if b.GetGeneration() > sinceGen {
			metas = append(metas, searchengine.SegmentMeta{
				ID: b.GetId(), Format: b.GetFormat(), Generation: b.GetGeneration(),
			})
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Generation < metas[j].Generation })
	return metas, nil
}

func (c *loadBoundSource) Fetch(_ context.Context, ids []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	c.mu.Lock()
	c.fetchCalls++
	if len(ids) > c.maxFetchIDs {
		c.maxFetchIDs = len(ids)
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []searchengine.SegmentBlob
	var total int
	for _, b := range c.blobs {
		if want[b.GetId()] {
			out = append(out, blobFromProto(b))
			total += len(b.GetBytes())
		}
	}
	ceiling := c.byteCeiling
	c.mu.Unlock()

	if ceiling > 0 && total > ceiling {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("byte ceiling: %d bytes over %d", total, ceiling))
	}
	return out, nil
}

func (c *loadBoundSource) Ship(_ context.Context, _ []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	return nil, nil
}

func (c *loadBoundSource) Prune(_ []searchengine.SegmentID) (int, error) { return 0, nil }

func (c *loadBoundSource) PublishManifest(_ string, _ []segmentDigest) (int, error) { return 0, nil }

func (c *loadBoundSource) verifiesCompletenessServerSide() bool { return false }

var _ segmentSource = (*loadBoundSource)(nil)

// seedMockSegments appends n mock-format segments (each a single-row mock segment
// so the loader's engine can Decode + Import them) with ascending generations and
// the given per-blob payload size. Returns the highest generation seeded.
func (c *loadBoundSource) seedMockSegments(t *testing.T, n, payloadBytes int) uint64 {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var maxGen uint64
	for i := range n {
		id := fmt.Sprintf("seg-%05d", i)
		// A decodable mock segment: one row whose content is padded to payloadBytes
		// so the byte total is realistic for the ceiling simulation.
		pad := make([]byte, payloadBytes)
		for j := range pad {
			pad[j] = 'a'
		}
		rows := []mockRow{{ID: id, Content: string(pad)}}
		body, err := json.Marshal(rows)
		require.NoError(t, err)
		c.blobs = append(c.blobs, &knowledgev1.SegmentBlobProto{
			Id: id, Format: "mock", Generation: uint64(i + 1), Bytes: body,
		})
		maxGen = uint64(i + 1)
	}
	return maxGen
}

func newLoadBoundManager(t *testing.T, src *loadBoundSource) *distManager[mockQuery, mockStats] {
	t.Helper()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "peakbound"}
	cache := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
	return newDistManager(newMockEngine(t), src, cache, target, "")
}

// TestLoadNeverExceedsFetchIDCap drives a real load() of M misses (M >>
// maxFetchSegmentIDs) and asserts no single Fetch requested more than
// maxFetchSegmentIDs ids, AND every listed segment was imported (no loss across
// sub-batches), AND importedGen advanced to the listed max.
func TestLoadNeverExceedsFetchIDCap(t *testing.T) {
	t.Parallel()

	src := &loadBoundSource{}
	const m = 3*maxFetchSegmentIDs + 11 // M well over the cap, not a clean multiple
	maxGen := src.seedMockSegments(t, m, 16)

	mgr := newLoadBoundManager(t, src)
	require.NoError(t, mgr.load(context.Background()))

	require.LessOrEqual(t, src.maxFetchIDs, maxFetchSegmentIDs,
		"no single Fetch may exceed the id cap")
	require.Positive(t, src.maxFetchIDs, "load must have Fetched the cold misses")

	// No loss: every seeded segment is searchable (each row content is "aaaa...").
	hits := mgr.engine.Search(mockQuery{term: "a"}, m+10)
	require.Len(t, hits, m, "every listed segment must be imported — no loss across sub-batches")
	require.Equal(t, maxGen, mgr.importedGen.Load(), "importedGen advances to the listed max after a complete load")
}

// TestLoadHalvesAndRetriesUnderByteCeiling drives a real load() against a src
// that rejects any Fetch whose summed bytes exceed a byte ceiling with
// ResourceExhausted, with a count-capped chunk's bytes STARTING above the ceiling.
// load() must halve+retry through fetchMisses, import EVERY listed id (no loss),
// and advance importedGen to listedMaxGen ONLY after the complete fetch.
func TestLoadHalvesAndRetriesUnderByteCeiling(t *testing.T) {
	t.Parallel()

	src := &loadBoundSource{}
	// Each blob ~ 1 KiB of content. A full count-capped chunk (256 ids) is ~256
	// KiB; set the ceiling to 32 KiB so the first chunk is rejected and must halve
	// several times until each sub-chunk's bytes fit.
	const perBlob = 1 << 10 // 1 KiB
	const m = 2 * maxFetchSegmentIDs
	maxGen := src.seedMockSegments(t, m, perBlob)
	src.byteCeiling = 32 << 10 // 32 KiB

	mgr := newLoadBoundManager(t, src)
	require.NoError(t, mgr.load(context.Background()),
		"load must halve+retry under the byte ceiling and complete")

	hits := mgr.engine.Search(mockQuery{term: "a"}, m+10)
	require.Len(t, hits, m, "every listed segment must be imported after halving — no loss")
	require.Equal(t, maxGen, mgr.importedGen.Load(),
		"importedGen advances to listedMaxGen only after the complete halve-and-retry fetch")
}

// TestLoadSingleBlobOverCeilingDoesNotAdvance is the negative case proving the
// unconditional importedGen advance is SAFE under Option A: when a SINGLE blob's
// bytes alone exceed the ceiling, fetchMisses hard-errors, load() returns the error
// BEFORE Import/advance, and importedGen does NOT move — so the unfetched id stays
// re-listable on the next load (no silent loss).
func TestLoadSingleBlobOverCeilingDoesNotAdvance(t *testing.T) {
	t.Parallel()

	src := &loadBoundSource{}
	// One fat blob whose bytes alone exceed the ceiling, plus some normal ones.
	const perBlob = 1 << 10 // 1 KiB normal
	src.seedMockSegments(t, 5, perBlob)
	// Make the LAST seeded blob enormous (over the ceiling on its own).
	src.mu.Lock()
	fat := make([]byte, 64<<10) // 64 KiB content, well over the ceiling below
	for j := range fat {
		fat[j] = 'a'
	}
	rows := []mockRow{{ID: searchengine.ExternalID("seg-fat"), Content: string(fat)}}
	body, err := json.Marshal(rows)
	require.NoError(t, err)
	src.blobs = append(src.blobs, &knowledgev1.SegmentBlobProto{
		Id: "seg-fat", Format: "mock", Generation: 6, Bytes: body,
	})
	src.mu.Unlock()
	src.byteCeiling = 8 << 10 // 8 KiB — the fat blob alone over-runs it

	mgr := newLoadBoundManager(t, src)
	require.Equal(t, uint64(0), mgr.importedGen.Load(), "precondition: cold loader")

	err = mgr.load(context.Background())
	require.Error(t, err, "a single blob over the ceiling must fail load()")
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, uint64(0), mgr.importedGen.Load(),
		"importedGen must NOT advance when the fetch hard-errors — the id stays re-listable")
}
