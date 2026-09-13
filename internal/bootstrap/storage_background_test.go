// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

func TestStorageReconcileGraphsRemainIndependent(t *testing.T) {
	c := &client{workingSet: workingset.New()}
	c.workingSet.AdmitRef(workingset.Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "local"}, "query")
	c.workingSet.AdmitRef(workingset.Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "cloud", Account: "a"}, "query")
	graphs := c.segmentBearingGraphs()
	require.Len(t, graphs, 2)
	require.NotEqual(t, graphs[0], graphs[1])
}

func TestStorageMergeWatermarksRemainIndependent(t *testing.T) {
	c := &client{segmentMgr: segmentdist.NewManager(t.TempDir(), 0)}
	t.Cleanup(func() { c.segmentMgr.Close() })
	local := segmentGraphRef{gt: kgtypes.GraphKnowledge, name: "default", destination: graphclient.Destination{Storage: "local"}}
	cloud := segmentGraphRef{gt: kgtypes.GraphKnowledge, name: "default", destination: graphclient.Destination{Storage: "cloud", AccountID: "a"}}
	c.commitMergeWatermark(local, mergePending{Pull: true, Horizon: 9})
	c.commitMergeWatermark(cloud, mergePending{Pull: true, Horizon: 2})
	a, err := c.segmentMgr.ForDestination(local.bind(t.Context())).LoadMergeWatermark(local.gt, local.name)
	require.NoError(t, err)
	b, err := c.segmentMgr.ForDestination(cloud.bind(t.Context())).LoadMergeWatermark(cloud.gt, cloud.name)
	require.NoError(t, err)
	require.Equal(t, int64(9), a)
	require.Equal(t, int64(2), b)
}

func TestStorageHealBreakersRemainIndependent(t *testing.T) {
	c := &client{}
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	cloud := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud", AccountID: "a"})
	for range healBreakerTripThreshold {
		c.classifyStorageHealOutcome(local, kgtypes.GraphKnowledge, "default", true, 0)
	}
	require.False(t, c.healBreaker.ForDestination(local).Allow(kgtypes.GraphKnowledge, "default"))
	require.True(t, c.healBreaker.ForDestination(cloud).Allow(kgtypes.GraphKnowledge, "default"))
	c.ClearStorageHealLatch(cloud, kgtypes.GraphKnowledge, "default")
	require.False(t, c.healBreaker.ForDestination(local).Allow(kgtypes.GraphKnowledge, "default"))
}

func TestStorageWorkingSetRemovalKeepsOtherCopy(t *testing.T) {
	c := &client{workingSet: workingset.New()}
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	cloud := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud", AccountID: "a"})
	c.AdmitDestinationGraph(local, kgtypes.GraphKnowledge, "default", "test")
	c.AdmitDestinationGraph(cloud, kgtypes.GraphKnowledge, "default", "test")
	require.True(t, c.RemoveStorageFromWorkingSet(local, kgtypes.GraphKnowledge, "default"))
	require.Len(t, c.workingSet.Members(), 1)
}

func TestStorageDropCacheRetainsOtherCopy(t *testing.T) {
	cacheRoot := t.TempDir()
	m := segmentdist.NewManager(cacheRoot, 0)
	t.Cleanup(func() { m.Close() })
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	cloud := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud", AccountID: "a"})
	for _, ctx := range []context.Context{local, cloud} {
		require.NoError(t, m.ReplaceBucketFields(ctx, kgtypes.GraphCode, "storage-repo", nil, []searchengine.Document{{ID: "same", Fields: map[string]string{searchengine.FieldContent: "needle"}}}))
	}
	_, err := (segmentCacheDropperAdapter{mgr: m}).ForStorage(local).DropGraphCache(kgtypes.GraphCode, "storage-repo")
	require.NoError(t, err)
	// DropGraphCache removes persisted files; a fresh reader excludes resident keys.
	fresh := segmentdist.NewManager(cacheRoot, 0)
	t.Cleanup(func() { fresh.Close() })
	require.Zero(t, fresh.ForDestination(local).CachedSegmentCount(kgtypes.GraphCode, "storage-repo", "bm25v2"))
	require.NotZero(t, fresh.ForDestination(cloud).CachedSegmentCount(kgtypes.GraphCode, "storage-repo", "bm25v2"))
}

func TestStorageBM25HealProgressRemainsIndependent(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	cloud := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud", AccountID: "a"})
	localKey := storageBM25HealKey(local, kgtypes.GraphCode, "same")
	cloudKey := storageBM25HealKey(cloud, kgtypes.GraphCode, "same")
	require.NotEqual(t, localKey, cloudKey)
	bm25HealMu.Lock()
	bm25HealPending[localKey] = 7
	bm25HealMu.Unlock()
	c := &client{}
	c.armStorageBM25HealProgress(cloud, kgtypes.GraphCode, "same")
	bm25HealMu.Lock()
	_, pending := bm25HealPending[localKey]
	bm25HealMu.Unlock()
	require.True(t, pending)
	c.armStorageBM25HealProgress(local, kgtypes.GraphCode, "same")
	clearStorageBM25HealProgress(cloud, kgtypes.GraphCode, "same")
	bm25HealMu.Lock()
	armed := bm25HealArmed[localKey]
	bm25HealMu.Unlock()
	require.Equal(t, 7, armed)
}
