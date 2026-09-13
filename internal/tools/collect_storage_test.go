// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCollectStorageSeparatesRunsAndEpochs(t *testing.T) {
	r := NewCollectRuntime()
	t.Cleanup(func() { r.Stop(time.Second) })
	local := r.ForDestination(graphclient.Destination{Storage: "local"})
	cloud := r.ForDestination(graphclient.Destination{Storage: "cloud", AccountID: "a"})
	release := make(chan struct{})
	h, started, _ := local.Start("same", "same", kgtypes.GraphCode, "same", func() (string, string, error) { <-release; return "", "same", nil })
	require.True(t, started)
	require.False(t, cloud.CollectInFlightForGraph(kgtypes.GraphCode, "same"))
	c, started, _ := cloud.Start("same", "same", kgtypes.GraphCode, "same", func() (string, string, error) { return "", "same", nil })
	require.True(t, started)
	<-c.Done()
	require.Equal(t, uint64(0), local.CompletedCollectsForGraph(kgtypes.GraphCode, "same"))
	require.Equal(t, uint64(1), cloud.CompletedCollectsForGraph(kgtypes.GraphCode, "same"))
	close(release)
	<-h.Done()
	snapshot := r.Snapshot()
	require.Len(t, snapshot, 2)
	require.NotEqual(t, snapshot[0].Destination, snapshot[1].Destination)
}

func TestCollectStorageStatusRetainsDestination(t *testing.T) {
	runs := []CollectRunStatus{{Label: "same", State: "completed", Destination: graphclient.Destination{Storage: "cloud", AccountID: "account-a"}}}
	require.Contains(t, renderCollectRunsText(runs), "cloud/account-a")
	result := map[string]any{}
	addCollectRunsJSON(result, runs)
	entry := result["collect_runs"].([]map[string]any)[0]
	require.Equal(t, "cloud", entry["storage"])
	require.Equal(t, "account-a", entry["account"])
}

func TestStorageRebuildFlightsRemainIndependent(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	cloud := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud", AccountID: "a"})
	go func() {
		defer close(done)
		_, _ = RebuildSegments(local, &blockingScanner{entered: entered, release: release}, &fakeRebuildShipper{}, kgtypes.GraphCode, "storage-rebuild", false)
	}()
	<-entered
	t.Cleanup(func() { close(release); <-done })
	out, err := RebuildSegments(cloud, &fakeRebuildScanner{}, &fakeRebuildShipper{}, kgtypes.GraphCode, "storage-rebuild", false)
	require.NoError(t, err)
	require.True(t, out.Ran, "cloud rebuild incorrectly joined local flight")
}
