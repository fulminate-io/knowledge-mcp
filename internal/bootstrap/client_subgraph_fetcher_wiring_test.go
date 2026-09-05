// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// TestConstructClient_SubgraphFetcherSharesTheSinksInnerUploader pins the
// construction wiring the tools package cannot see: every tools test satisfies
// SubgraphFetcher by injection, so nothing over there notices whether the real
// constructor assigns the field at all.
//
// Both assertions are load-bearing, and each rejects a different
// wrong-but-compiling constructor:
//   - a constructor that never assigns c.subgraphFetcher compiles and passes
//     every tools test, while the live daemon fails every collect(type:"logs")
//     at the accessor's nil guard;
//   - a constructor that mints a SECOND uploader satisfies the nil check while
//     splitting one collect across two sinks with two epoch sequences.
//
// Seam-override idiom mirrors client_keepalive_gate_test.go: override the
// package vars, restore via t.Cleanup. No RPC is issued, so nothing dials.
func TestConstructClient_SubgraphFetcherSharesTheSinksInnerUploader(t *testing.T) {
	dialer := func(int) *graphclient.GraphClient {
		gc := graphclient.NewGraphClientForURL("http://local.invalid")
		t.Cleanup(gc.Close)
		return gc
	}

	origStore := newAuthStoreFn
	newAuthStoreFn = func() (auth.Store, error) { return newFakeAuthStore(), nil }
	t.Cleanup(func() { newAuthStoreFn = origStore })

	origKeepalive := startKeepaliveFn
	startKeepaliveFn = func(_ *graphclient.GraphClient, _ context.Context) {}
	t.Cleanup(func() { startKeepaliveFn = origKeepalive })

	c := constructClient(Config{LocalDialer: dialer})
	require.NotNil(t, c)

	fetcher := c.SubgraphFetcher()
	require.NotNil(t, fetcher,
		"constructClient must wire the cloud subgraph fetcher; a nil one fails every collect(type:\"logs\") at the accessor guard")

	wrapper, ok := c.Sink().(admittingSink)
	require.True(t, ok, "the collect sink must still be the admitting wrapper; got %T", c.Sink())
	assert.Same(t, fetcher, wrapper.inner,
		"the fetcher and the wrapper's inner sink must be the SAME uploader instance — a second uploader splits one collect across two sinks")
}
