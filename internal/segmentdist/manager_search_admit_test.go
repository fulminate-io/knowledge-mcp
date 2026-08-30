// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestSearch_RecordsGraphAdmission pins that a user search admits the graph it
// searched, and that a k<=0 call does not. The k<=0 half is the catcher for
// placing the recorder ABOVE the guard: such a call is not a user search, and a
// recorder above the guard would admit a graph nobody actually searched.
func TestSearch_RecordsGraphAdmission(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		admitted []string
	)
	record := func(gt kgtypes.GraphType, name string) {
		mu.Lock()
		defer mu.Unlock()
		admitted = append(admitted, string(gt)+"/"+name)
	}
	recorded := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), admitted...)
	}

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0, WithGraphAdmitter(record)))

	// A k<=0 call is not a user search. Asserted BEFORE the real search so the
	// positive case below cannot mask an admission recorded here.
	_, _ = mgr.Search(context.Background(), kgtypes.GraphCode, "repoA", "hello", nil, 0)
	require.Equal(t, []string(nil), recorded(),
		"a k<=0 call is not a user search and must admit nothing")

	// A real search. The engines are cold and there are no segments, so the
	// result is empty rather than an error — the admission is what this test is
	// about, and it happens before any load.
	_, _ = mgr.Search(context.Background(), kgtypes.GraphCode, "repoA", "hello", nil, 10)
	assert.Equal(t, []string{"code/repoA"}, recorded(),
		"a user search IS the direct interaction that admits a graph")
}
