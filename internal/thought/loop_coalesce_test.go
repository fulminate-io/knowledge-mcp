// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBackstopCoalesce_TickAbsorbed pre-claims the reflection single-flight guard
// on a holder, then drives one runBackgroundPropagation tick. The tick must
// COALESCE: its body (fetchAdjacency, the type=thought browse) must NOT run because
// the guard is already held. Fails-when-absent the loop-tick guard — without the
// AcquireReflectionPass claim the tick drains the corpus while a pass is in flight.
func TestBackstopCoalesce_TickAbsorbed(t *testing.T) {
	// Hold the guard for the duration of the tick.
	release, ok := AcquireReflectionPass(ReflectionPassKey)
	require.True(t, ok, "test must win the first claim")
	defer release()

	fake := &gateFake{probeGen: 9} // a bumped gen would otherwise force the body to run.
	newGateLoop(fake).runBackgroundPropagation()

	assert.False(t, fake.didPassRun(),
		"a tick fired while the reflection guard is held must coalesce — no fetchAdjacency drain")
}

// TestBackstopCoalesce_TickRunsWhenFree confirms the guard does NOT wedge the
// loop: with the key free the tick runs its body normally. Guards against a guard
// bug that never releases.
func TestBackstopCoalesce_TickRunsWhenFree(t *testing.T) {
	fake := &gateFake{probeGen: 9} // bumped gen → body runs (no quiet-skip).

	newGateLoop(fake).runBackgroundPropagation()

	assert.True(t, fake.didPassRun(),
		"with the reflection guard free a bumped-gen tick must run its body")
}

// TestBootDetectionCoalesce pre-claims the reflection guard on a holder, then
// invokes the guarded boot detection path (runBootClusterDetection, the helper
// Start calls). The boot detection must NOT drain while the key is held.
// Fails-when-absent the T3-b boot-path guard — if Start called runClusterDetection
// without claiming the guard the boot drain would race an in-flight manual pass.
func TestBootDetectionCoalesce(t *testing.T) {
	release, ok := AcquireReflectionPass(ReflectionPassKey)
	require.True(t, ok, "test must win the first claim")
	defer release()

	fake := &gateFake{}
	newGateLoop(fake).runBootClusterDetection()

	assert.False(t, fake.didPassRun(),
		"boot cluster detection fired while the reflection guard is held must coalesce — no fetchAdjacency drain")
}
