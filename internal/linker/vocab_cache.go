// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"fmt"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// vocab_cache.go makes a linker pass read the linkage graph's edge vocabulary
// ONCE, however many edges the pass emits.
//
// WHY IT EXISTS: crossgraph.ResolveAndLink resolves once per CALL, which is
// right for the interactive path — one mutate(link) is one link — and wrong
// here. emitLink sits inside a per-item loop at every linker entry point, so
// without this a pass costs one Stats RPC per emitted edge. Measured before the
// cache existed: 1, 5 and 25 COPY directives cost 1, 5 and 25 Stats RPCs. The
// pre-change linker cost ZERO, because the edge type was folded locally — so
// absent this cache the change would INTRODUCE an N+1 rather than inherit one.

// statsCaller is the linker-local narrow view of a client that serves Stats.
// Declared here rather than widening the package's Execute-only GraphCaller,
// which is the same narrowing asExecutor makes for the Execute seam.
type statsCaller interface {
	Stats(ctx context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error)
}

// linkerStatsFnOf upgrades a GraphCaller to an engine.StatsFn or returns a
// typed error, so a caller without the seam is loud rather than silently
// unresolved.
func linkerStatsFnOf(gc GraphCaller) (engine.StatsFn, error) {
	sc, ok := gc.(statsCaller)
	if !ok {
		return nil, fmt.Errorf(
			"linker: graph client %T serves no Stats RPC, so edge types cannot be resolved against the linkage graph's vocabulary", gc)
	}
	return sc.Stats, nil
}

// vocabCachingCaller wraps a GraphCaller so that Stats is answered from ONE
// snapshot per pass while Execute passes straight through.
//
// IT EMBEDS THE INTERFACE, NOT THE CONCRETE CLIENT, so the wrapper's method set
// is exactly Execute plus Stats — a downstream type assertion for any OTHER
// method the concrete client carries (Call, Healthy, ...) FAILS on the wrapper
// where it would succeed on the unwrapped value. That is fine today and was
// checked rather than assumed: Execute and Stats are the ONLY two assertions any
// wrapped value reaches, enumerated across the corpus rather than sampled —
// Execute-only (asExecutor here, crossgraph.GraphCaller, the render fetch
// helpers) or Stats (linkerStatsFnOf and this type's own Stats).
// TestVocabCacheWrapperMethodSetIsExecutePlusStats pins both, with a Call leg as
// its control so the pin is discriminating. A pass that later
// needs a wider seam must widen this wrapper too, not assume it is transparent.
//
// THE MEMO CACHES ITS ERROR TOO, and that is a decision rather than an
// oversight. A FAILED first fetch is memoized exactly as a successful one is,
// so every later emit in the pass sees that same failure and the underlying
// seam is called at most once. The alternative — memoize only a success and
// retry on the next emit — also compiles and also satisfies a plain reading of
// "memoizes Stats". The two ship materially different behaviour, measured over
// a five-directive pass whose Stats seam fails its FIRST call and succeeds
// afterwards: caching the error gives 1 underlying read and 0 of 5 edges
// composed, while memoizing only successes gives 2 underlying reads and 4 of 5.
// Caching the error is what "one Stats read per PASS" says literally under all
// conditions, and it is the only reading under which one pass cannot resolve
// different edges against DIFFERENT vocabulary snapshots — which is the
// two-casing-families hazard this whole change exists to remove.
//
// ONE CACHE WITH NO KEY is correct here, and the reason is verified rather than
// assumed: emitLink hardcodes TargetGraph "linkage", so every link in every
// pass resolves against the same graph. If a future caller emits into a second
// graph this cache becomes wrong — keyless is a property to re-check then, not
// a license.
type vocabCachingCaller struct {
	GraphCaller

	// once/resp/err memoize the single vocabulary snapshot; reads counts how
	// many times the UNDERLYING seam was actually called. The counter is
	// incremented inside the fetch and never in the accessor, mirroring
	// recipe's sourceView census memo, so "read once per pass" is a MEASUREMENT
	// a test can assert rather than a restatement of what sync.Once guarantees
	// structurally.
	once  sync.Once
	resp  *knowledgev1.StatsResponse
	err   error
	reads int
}

// Stats answers every call in the pass from one snapshot.
//
// The underlying seam is resolved LAZILY, inside the Once — NOT eagerly in
// withVocabCache. Eager resolution reads as good fail-fast hygiene and is
// measurably wrong: it makes a Stats-less caller abort BEFORE the linker's
// discovery queries, so a pass that previously reached the wire and failed at
// emit does no work at all. TestRunPostCollectLinker_GatedByCollectorType
// (across all eight collector arms) and TestRunPostCollectLinker_RunError_BestEffort
// both go red on the eager version. Fail-fast is not free when something
// downstream already depends on how far the work got.
func (v *vocabCachingCaller) Stats(ctx context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	v.once.Do(func() {
		v.reads++
		sc, ok := v.GraphCaller.(statsCaller)
		if !ok {
			v.err = fmt.Errorf(
				"linker: graph client %T serves no Stats RPC, so edge types cannot be resolved against the linkage graph's vocabulary", v.GraphCaller)
			return
		}
		v.resp, v.err = sc.Stats(ctx, req)
	})
	return v.resp, v.err
}

// withVocabCache wraps gc so one pass reads the edge vocabulary once. Each pass
// entry point wraps its gc ONCE on entry and shadows the parameter; emitLink
// keeps its signature and derives its Stats seam from gc, so wrapping gc is
// what redirects it.
func withVocabCache(gc GraphCaller) GraphCaller {
	return &vocabCachingCaller{GraphCaller: gc}
}
