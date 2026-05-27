// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolutionMap_RoundTrip(t *testing.T) {
	rm := NewResolutionMap()

	// Empty map: lookup returns ("", false).
	got, ok := rm.Lookup("missing")
	assert.False(t, ok)
	assert.Empty(t, got)

	// Record then lookup.
	rm.Record("aks-dev", "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-dev")
	got, ok = rm.Lookup("aks-dev")
	assert.True(t, ok)
	assert.Equal(t, "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-dev", got)

	// Empty resolution is a no-op (does not clobber).
	rm.Record("aks-dev", "")
	got, ok = rm.Lookup("aks-dev")
	assert.True(t, ok)
	assert.Equal(t, "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-dev", got)

	// Empty resolution on a missing key is also a no-op (still missing).
	rm.Record("nope", "")
	_, ok = rm.Lookup("nope")
	assert.False(t, ok)

	// Multiple keys are independent.
	rm.Record("other", "/subscriptions/x/.../managedClusters/other")
	got, ok = rm.Lookup("other")
	assert.True(t, ok)
	assert.Equal(t, "/subscriptions/x/.../managedClusters/other", got)
}

func TestResolutionMap_ContextRoundTrip(t *testing.T) {
	// Naked ctx: no map, ResolutionMapFrom returns nil.
	naked := context.Background()
	assert.Nil(t, ResolutionMapFrom(naked))

	// Install a map; ResolutionMapFrom returns the same pointer.
	rm := NewResolutionMap()
	ctx := WithResolutionMap(naked, rm)
	got := ResolutionMapFrom(ctx)
	assert.Same(t, rm, got, "ResolutionMapFrom must return the same pointer that was installed")

	// Round-trip through ctx: writes from one handle visible via the other.
	rm.Record("k", "v")
	v, ok := got.Lookup("k")
	assert.True(t, ok)
	assert.Equal(t, "v", v)
}
