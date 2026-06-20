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

// loadBoundCaller is an in-memory segmentCaller that backs ListDelta + Fetch from
// a seeded blob set and instruments Fetch for the peak-memory-bound assertions:
//   - records the id count of every Fetch call (maxFetchIDs),
//   - optionally rejects any Fetch whose summed content bytes exceed byteCeiling
//     with connect.CodeResourceExhausted (the server byte-ceiling stand-in), which
//     drives fetchMisses' adaptive halving through the real load() path.
//
// Each seeded blob carries a real byte payload so the byte-ceiling simulation is
// faithful (the cap is about bytes, not ids).
type loadBoundCaller struct {
	mu          sync.Mutex
	blobs       []*knowledgev1.SegmentBlobProto // generation-ordered
	byteCeiling int                             // 0 = never reject
	maxFetchIDs int                             // largest id count seen in a single Fetch
	fetchCalls  int
}

func (c *loadBoundCaller) Ship(_ context.Context, _ *knowledgev1.ShipRequest) (*knowledgev1.ShipResponse, error) {
	return &knowledgev1.ShipResponse{}, nil
}

func (c *loadBoundCaller) ListDelta(_ context.Context, req *knowledgev1.ListDeltaRequest) (*knowledgev1.ListDeltaResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var metas []*knowledgev1.SegmentMetaProto
	for _, b := range c.blobs {
		if b.GetGeneration() > req.GetSinceGen() {
			metas = append(metas, &knowledgev1.SegmentMetaProto{
				Id: b.GetId(), Format: b.GetFormat(), Generation: b.GetGeneration(),
			})
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].GetGeneration() < metas[j].GetGeneration() })
	return &knowledgev1.ListDeltaResponse{Metas: metas}, nil
}

func (c *loadBoundCaller) Fetch(_ context.Context, req *knowledgev1.FetchRequest) (*knowledgev1.FetchResponse, error) {
	ids := req.GetIds()
	c.mu.Lock()
	c.fetchCalls++
	if len(ids) > c.maxFetchIDs {
		c.maxFetchIDs = len(ids)
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []*knowledgev1.SegmentBlobProto
	var total int
	for _, b := range c.blobs {
		if want[b.GetId()] {
			out = append(out, b)
			total += len(b.GetBytes())
		}
	}
	ceiling := c.byteCeiling
	c.mu.Unlock()

	if ceiling > 0 && total > ceiling {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("byte ceiling: %d bytes over %d", total, ceiling))
	}
	return &knowledgev1.FetchResponse{Blobs: out}, nil
}

func (c *loadBoundCaller) Prune(_ context.Context, _ *knowledgev1.PruneRequest) (*knowledgev1.PruneResponse, error) {
	return &knowledgev1.PruneResponse{}, nil
}

func (c *loadBoundCaller) Publish(_ context.Context, _ *knowledgev1.PublishRequest) (*knowledgev1.PublishResponse, error) {
	return &knowledgev1.PublishResponse{}, nil
}

// seedMockSegments appends n mock-format segments (each a single-row mock segment
// so the loader's engine can Decode + Import them) with ascending generations and
// the given per-blob payload size. Returns the highest generation seeded.
func (c *loadBoundCaller) seedMockSegments(t *testing.T, n, payloadBytes int) uint64 {
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

func newLoadBoundManager(t *testing.T, caller *loadBoundCaller) *distManager[mockQuery, mockStats] {
	t.Helper()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "peakbound"}
	src := newRPCSegmentSource(caller, target, "", context.Background())
	cache := newDiskSegmentCache(t.TempDir(), 0)
	return newDistManager(newMockEngine(), src, cache, target, "")
}

// TestLoadNeverExceedsFetchIDCap drives a real load() of M misses (M >>
// maxFetchSegmentIDs) and asserts no single Fetch requested more than
// maxFetchSegmentIDs ids, AND every listed segment was imported (no loss across
// sub-batches), AND importedGen advanced to the listed max.
func TestLoadNeverExceedsFetchIDCap(t *testing.T) {
	caller := &loadBoundCaller{}
	const m = 3*maxFetchSegmentIDs + 11 // M well over the cap, not a clean multiple
	maxGen := caller.seedMockSegments(t, m, 16)

	mgr := newLoadBoundManager(t, caller)
	require.NoError(t, mgr.load(context.Background()))

	require.LessOrEqual(t, caller.maxFetchIDs, maxFetchSegmentIDs,
		"no single Fetch may exceed the id cap")
	require.Positive(t, caller.maxFetchIDs, "load must have Fetched the cold misses")

	// No loss: every seeded segment is searchable (each row content is "aaaa...").
	hits := mgr.engine.Search(mockQuery{term: "a"}, m+10)
	require.Len(t, hits, m, "every listed segment must be imported — no loss across sub-batches")
	require.Equal(t, maxGen, mgr.importedGen.Load(), "importedGen advances to the listed max after a complete load")
}

// TestLoadHalvesAndRetriesUnderByteCeiling drives a real load() against a caller
// that rejects any Fetch whose summed bytes exceed a byte ceiling with
// ResourceExhausted, with a count-capped chunk's bytes STARTING above the ceiling.
// load() must halve+retry through fetchMisses, import EVERY listed id (no loss),
// and advance importedGen to listedMaxGen ONLY after the complete fetch.
func TestLoadHalvesAndRetriesUnderByteCeiling(t *testing.T) {
	caller := &loadBoundCaller{}
	// Each blob ~ 1 KiB of content. A full count-capped chunk (256 ids) is ~256
	// KiB; set the ceiling to 32 KiB so the first chunk is rejected and must halve
	// several times until each sub-chunk's bytes fit.
	const perBlob = 1 << 10 // 1 KiB
	const m = 2 * maxFetchSegmentIDs
	maxGen := caller.seedMockSegments(t, m, perBlob)
	caller.byteCeiling = 32 << 10 // 32 KiB

	mgr := newLoadBoundManager(t, caller)
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
	caller := &loadBoundCaller{}
	// One fat blob whose bytes alone exceed the ceiling, plus some normal ones.
	const perBlob = 1 << 10 // 1 KiB normal
	caller.seedMockSegments(t, 5, perBlob)
	// Make the LAST seeded blob enormous (over the ceiling on its own).
	caller.mu.Lock()
	fat := make([]byte, 64<<10) // 64 KiB content, well over the ceiling below
	for j := range fat {
		fat[j] = 'a'
	}
	rows := []mockRow{{ID: searchengine.ExternalID("seg-fat"), Content: string(fat)}}
	body, err := json.Marshal(rows)
	require.NoError(t, err)
	caller.blobs = append(caller.blobs, &knowledgev1.SegmentBlobProto{
		Id: "seg-fat", Format: "mock", Generation: 6, Bytes: body,
	})
	caller.mu.Unlock()
	caller.byteCeiling = 8 << 10 // 8 KiB — the fat blob alone over-runs it

	mgr := newLoadBoundManager(t, caller)
	require.Equal(t, uint64(0), mgr.importedGen.Load(), "precondition: cold loader")

	err = mgr.load(context.Background())
	require.Error(t, err, "a single blob over the ceiling must fail load()")
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, uint64(0), mgr.importedGen.Load(),
		"importedGen must NOT advance when the fetch hard-errors — the id stays re-listable")
}
