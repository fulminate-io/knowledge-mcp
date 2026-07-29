// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// corpus_memory_test.go measures the resident-memory footprint of the daemon
// thought-corpus cache over the representative corpus (10k thoughts + 5.2k charges +
// 3k sessions), and asserts the 1-change → 1-row delta path lands end-to-end through
// a REWIRED consumer (verified against the rewired read rather than only the cache
// internals).

// TestCorpusCacheResidentMemory measures the HeapAlloc the resident cache adds for
// the representative corpus and records it via t.Logf. It is a MEASUREMENT (a loose
// upper-bound assert guards against an accidental order-of-magnitude regression, not
// a tight budget) — the documented figure is the deliverable.
func TestCorpusCacheResidentMemory(t *testing.T) {
	const (
		nThoughts = 10_000
		nCharges  = 5_200
		nSessions = 3_000
	)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	// Build + merge AFTER the baseline so the delta counts the FULL resident
	// footprint the cache retains — the node payloads (which would otherwise be GC'd
	// after the drain) plus the id-map, not just the map overhead.
	nodes := buildRepresentativeCorpus(nThoughts, nCharges, nSessions)
	cache := newCorpusCache()
	cache.MergeDelta(&knowledgev1.CorpusDeltaResponse{Items: nodes})

	runtime.GC()
	runtime.ReadMemStats(&after)

	// Keep the cache + nodes reachable across the second reading so their retained
	// heap is counted (not collected as dead).
	require.Len(t, cache.Snapshot(), nThoughts+nCharges+nSessions)
	runtime.KeepAlive(nodes)

	total := nThoughts + nCharges + nSessions
	deltaBytes := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	perNode := float64(deltaBytes) / float64(total)
	t.Logf("resident corpus cache: %d nodes (%d thoughts + %d charges + %d sessions) → %d bytes resident (%.0f MiB), ~%.0f bytes/node",
		total, nThoughts, nCharges, nSessions, deltaBytes, float64(deltaBytes)/(1024*1024), perNode)

	// Loose sanity bound: the resident set must be positive and well under 200 MiB
	// for ~18k modestly-sized nodes — a regression that blows this is an accidental
	// deep-copy / retention leak, not a normal cache.
	assert.Positive(t, deltaBytes, "the resident cache retains heap for the corpus")
	assert.Less(t, deltaBytes, int64(200*1024*1024), "resident footprint stays well under 200 MiB for ~18k nodes")
}

// buildRepresentativeCorpus builds a mixed thought/charge/session node set shaped
// like the real corpus: thoughts carry cluster_id + propagated_* + session
// metadata, charges carry polarity + weight, sessions carry a label.
func buildRepresentativeCorpus(nThoughts, nCharges, nSessions int) []*knowledgev1.Node {
	nodes := make([]*knowledgev1.Node, 0, nThoughts+nCharges+nSessions)
	for i := range nThoughts {
		n := &knowledgev1.Node{
			Id:         fmt.Sprintf("thought-%06d", i),
			Type:       string(kgtypes.NodeThought),
			SymbolName: fmt.Sprintf("hypothesis about subsystem %d behavior under load", i%500),
			UpdatedAt:  int64(1_700_000_000_000_000_000 + i),
		}
		kgtypes.SetValue(n, metaClusterID, fmt.Sprintf("cluster-%d", i%400))
		kgtypes.SetValue(n, "propagated_valence", "0.4213")
		kgtypes.SetValue(n, "propagated_magnitude", "1.8765")
		kgtypes.SetValue(n, "session", fmt.Sprintf("session-%d", i%nSessions))
		nodes = append(nodes, n)
	}
	for i := range nCharges {
		c := &knowledgev1.Node{
			Id:        fmt.Sprintf("charge-%06d", i),
			Type:      string(kgtypes.NodeCharge),
			UpdatedAt: int64(1_700_000_000_000_000_000 + i),
		}
		kgtypes.SetValue(c, "polarity", []string{"positive", "negative"}[i%2])
		kgtypes.SetValue(c, "weight", "5")
		nodes = append(nodes, c)
	}
	for i := range nSessions {
		s := &knowledgev1.Node{
			Id:         fmt.Sprintf("session-%06d", i),
			Type:       string(kgtypes.NodeThoughtSession),
			SymbolName: fmt.Sprintf("session-%d", i),
			UpdatedAt:  int64(1_700_000_000_000_000_000 + i),
		}
		nodes = append(nodes, s)
	}
	return nodes
}

// TestDirtyTickOneRowThroughRewiredConsumer (criterion b, end-to-end) proves the
// 1-change → 1-row delta reaches a REWIRED consumer: after a warm cache absorbs one
// new thought via a single dirty-tick CorpusDelta, DetectPersistedClusters —
// reading through the resident cache (CorpusSource), never re-draining — reflects
// exactly that new thought's cluster, one CorpusDelta call total.
func TestDirtyTickOneRowThroughRewiredConsumer(t *testing.T) {
	// The rewired consumer reads clusters via the cache; it also issues bulk
	// charge/hydrate reads over the wire — a nil-returning gc keeps those best-effort
	// (the cluster partition comes purely from the cached nodes' cluster_id).
	gc := &nilCaller{}

	fake := &fakeCorpusScanner{
		rows:   []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}},
		freshH: 10_000_000,
	}
	p := (&PropagationLoop{gc: gc}).WithCorpusScanner(fake)
	// Seed the two warm rows with a cluster_id so DetectPersistedClusters partitions
	// them (the scanner emits bare nodes; stamp cluster_id in the resident cache).
	p.refreshCorpusCache(context.Background())
	stampClusterID(p.corpus, map[string]string{"t1": "cX", "t2": "cX"})

	warm, _ := p.CorpusSnapshot()
	require.Len(t, warm, 2, "cache warm with the two thoughts")

	fake.calls = 0
	// ONE new thought arrives at the cursor frontier.
	fake.rows = append(fake.rows, corpusRow{"t3", 3000, false})
	p.refreshCorpusCache(context.Background())
	stampClusterID(p.corpus, map[string]string{"t3": "cX"})

	assert.Equal(t, 1, fake.calls, "a 1-change dirty tick is exactly ONE CorpusDelta call — not a full re-drain")

	// The REWIRED consumer now sees all three thoughts from the cache (no drain).
	clusters, err := DetectPersistedClusters(context.Background(), gc, p)
	require.NoError(t, err)
	require.Len(t, clusters, 1, "one cluster")
	assert.Equal(t, 3, clusters[0].Size, "the rewired consumer reflects the one new thought merged this tick")
}

// nilCaller is a Caller whose Execute is a no-op returning empty — the rewired
// cluster read gets its partition from the resident cache, so the wire reads
// (charges/hydrate) degrade to empty without changing the partition.
type nilCaller struct{}

func (nilCaller) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// stampClusterID writes cluster_id onto the resident cache's live nodes in place,
// modeling the persisted cluster_id the delta feed carries in production.
func stampClusterID(c *corpusCache, byID map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, cid := range byID {
		if n, ok := c.live[id]; ok {
			kgtypes.SetValue(n, metaClusterID, cid)
		}
	}
}
