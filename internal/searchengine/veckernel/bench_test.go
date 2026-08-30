// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"testing"
)

// bench_test.go is the standing benchmark suite: the traverse simulation that
// prices the kernel the way a real graph query does, plus per-dim microbenches.
//
// EVERY BENCHMARK NAMES ITS TIER AND FORCES IT. A benchmark that measures
// "whatever dispatch picked" produces a number nobody can attribute, and worse,
// produces the SAME number whether the assembly ran or silently did not.
//
// Both report a custom ns/distance metric rather than leaving the reader to
// divide. ns/op is per benchmark iteration, and an iteration here is a whole
// hop of 64 candidates — reporting only ns/op invites a 64x misreading against
// the pinned table.

// --- the traverse simulation ------------------------------------------------

// traverseCorpus is a graph-shaped benchmark fixture: a flat vector block that
// does not fit in cache, plus a neighbor list per node.
type traverseCorpus struct {
	block []float32
	dim   int
	nodes int
	// neighbors is nodes * perNode, row-major.
	neighbors []uint32
	// perNode is how many neighbor ids each node's row carries. It is a field
	// rather than mMax0 read directly so a fixture can carry a different width
	// without every reader silently assuming 64.
	perNode int
}

const (
	// corpusFloorBytes is the smallest corpus this package will ever measure on,
	// regardless of cache size. 128 MiB was the fixed size before the corpus was
	// scaled, and every pin taken at or above it stays comparable.
	corpusFloorBytes = 128 << 20

	// corpusCacheMultiple is how many times the host's largest reported cache
	// the corpus must be. FOUR, with two as the absolute minimum the gate
	// accepts.
	//
	// A CORPUS THAT FITS IN CACHE MEASURES A KERNEL NOBODY WILL EVER RUN, and
	// the fixed 128 MiB was already failing that on one measured class: a GCE
	// c3-standard-8 has a 105 MiB L3, so the corpus was 1.22x the cache and the
	// traverse was mostly L3-resident. The dim 1024 number it produced implied
	// about 104 GB/s per core, which is not a memory-bandwidth figure — it is a
	// cache figure wearing a kernel's name, and the dispatch preference was read
	// off cells like it.
	corpusCacheMultiple = 4

	// mMax0 is the layer-0 neighbor count the graph builder uses, so a hop
	// scores exactly this many candidates.
	mMax0 = 64
)

// corpusBytes is the vector-block size for this host: the larger of the floor
// and corpusCacheMultiple times the biggest cache the OS reports.
//
// IT REFUSES RATHER THAN GUESSES. A platform whose cache size cannot be read
// gets an error, not a default, because a default is precisely how a
// cache-resident measurement gets published as a kernel measurement. Both timing
// entry points — the floor gate and the harvester — surface that error and
// decline to measure.
func corpusBytes() (int, error) {
	cache, ok := largestCacheBytes()
	if !ok {
		return 0, &CorpusSizingError{Source: cacheSource}
	}
	return max(cache*corpusCacheMultiple, corpusFloorBytes), nil
}

// CorpusSizingError means the host's cache size could not be determined, so no
// corpus can be sized against it.
type CorpusSizingError struct{ Source string }

func (e *CorpusSizingError) Error() string {
	return "veckernel: cannot size the benchmark corpus because this host's cache size is " +
		"unreadable (tried: " + e.Source + "). Refusing to measure: a corpus not provably " +
		"larger than the last-level cache prices the cache, not the kernel."
}

var (
	corpusOnce  sync.Map // dim -> *sync.Once
	corpusCache sync.Map // dim -> *traverseCorpus
)

// buildTraverseCorpus memoises per dim. Generating hundreds of MiB of float32
// takes long enough that rebuilding it per tier per benchmark would dominate the
// run.
//
// PANICS if the corpus cannot be sized, which is the only honest response for
// its MEASURING callers: a measurement on an unsized corpus is a number about
// the cache. The timing entry points check corpusBytes themselves first and
// report the error as a loud skip, so this panic is the backstop for a measuring
// caller that forgot to. Correctness gates need a corpus but take no timing;
// they go through requireTraverseCorpus instead and skip rather than panic.
func buildTraverseCorpus(dim int) *traverseCorpus {
	onceAny, _ := corpusOnce.LoadOrStore(dim, &sync.Once{})
	onceAny.(*sync.Once).Do(func() {
		size, err := corpusBytes()
		if err != nil {
			panic(err.Error())
		}
		nodes := size / (dim * 4)
		r := rand.New(rand.NewPCG(0x5eed, uint64(dim)))

		block := make([]float32, nodes*dim)
		for i := range block {
			block[i] = r.Float32()*2 - 1
		}
		neighbors := make([]uint32, nodes*mMax0)
		for i := range neighbors {
			neighbors[i] = uint32(r.IntN(nodes))
		}
		corpusCache.Store(dim, &traverseCorpus{
			block: block, dim: dim, nodes: nodes, neighbors: neighbors, perNode: mMax0,
		})
	})
	c, _ := corpusCache.Load(dim)
	return c.(*traverseCorpus)
}

// requireTraverseCorpus is buildTraverseCorpus for callers that are NOT
// measurements — the correctness gates, which need a corpus of some size but do
// not care what it prices.
//
// IT EXISTS SO THOSE CALLERS DO NOT INHERIT buildTraverseCorpus's PANIC. On a
// host whose cache topology is unreadable the panic took down the whole package
// test binary, so one unsizeable corpus turned every remaining test in the
// package into a single unexplained crash instead of a skip naming the cause.
// Observed on the lima/VZ arm64 CI guests, where it also took
// TestAllTiersWalkToTheSameTerminalNode with it.
//
// Under PerfEnv the refusal stays HARD: a measuring run on an unsizeable host is
// an error, not something to step around.
func requireTraverseCorpus(t *testing.T, dim int) *traverseCorpus {
	t.Helper()
	if _, err := corpusBytes(); err != nil {
		if envEnabled(PerfEnv) {
			t.Fatalf("%v", err)
		}
		t.Skipf("CORRECTNESS GATE NOT RUN. %v This test needs a corpus but takes no timing, so "+
			"the sizing refusal is disclosed as a skip here rather than as a failure; set %s=1 "+
			"to make it an error instead.", err, PerfEnv)
	}
	return buildTraverseCorpus(dim)
}

// TestCorpusIsLargerThanThisHostsCache is the gate on the sizing rule itself.
//
// It runs on every ordinary `go test`, with no env gate and no timing, because
// the failure it catches is silent by nature: a corpus that fits in cache
// produces plausible, fast, entirely wrong numbers, and nothing else in the
// suite would notice.
func TestCorpusIsLargerThanThisHostsCache(t *testing.T) {
	cache, ok := largestCacheBytes()
	if !ok {
		// A MEASURING run on a host with no readable cache topology is a hard
		// error: it asks for numbers that provably cannot be attributed.
		// WITHOUT PerfEnv no timing is taken anywhere in the run, so nothing
		// depends on the sizing rule and the outcome is a DISCLOSED skip rather
		// than a red. The red form failed the whole client suite on every
		// virtualised runner: lima/VZ arm64 guests publish
		// /sys/devices/system/cpu/cpu0/cache/index*/ carrying level and type but
		// NO size attribute, so the glob matches nothing and no corpus on such a
		// host can be shown to be out-of-cache — a property of the hypervisor,
		// not of this package, and not one any CI machine can cure.
		if envEnabled(PerfEnv) {
			t.Fatalf("this host's cache size is unreadable (tried: %s), so no corpus here can be "+
				"shown to be out-of-cache and every timing number this package produces on it "+
				"would be unattributable", cacheSource)
		}
		t.Skipf("CORPUS SIZING NOT CHECKED: this host's cache size is unreadable (tried: %s), so "+
			"no corpus here can be shown to be out-of-cache. No timing is taken without %s=1, so "+
			"nothing in this run rests on the sizing rule; run this on a host with readable cache "+
			"topology, or set %s=1 there, to gate it.", cacheSource, PerfEnv, PerfEnv)
	}
	size, err := corpusBytes()
	if err != nil {
		t.Fatalf("corpusBytes refused despite a readable cache size: %v", err)
	}

	multiple := float64(size) / float64(cache)
	t.Logf("largest reported cache %d MiB (%s); corpus %d MiB; multiple %.1fx",
		cache>>20, cacheSource, size>>20, multiple)

	if multiple < 2 {
		t.Errorf("corpus is only %.2fx the largest reported cache (%d MiB corpus vs %d MiB "+
			"cache). Below 2x the traverse is substantially cache-resident and its ns/distance "+
			"is a cache figure rather than a kernel one — which is exactly how a 105 MiB-L3 "+
			"part produced an implied 104 GB/s per core and had a dispatch preference read off "+
			"it.", multiple, size>>20, cache>>20)
	}
	if size < corpusFloorBytes {
		t.Errorf("corpus %d MiB is below the %d MiB floor; pins taken above the floor are not "+
			"comparable with it", size>>20, corpusFloorBytes>>20)
	}
}

// runTraverseSim walks hops distances-per-hop candidates and returns the number
// of distances computed. The next hop is the ARGMAX OF THE SCORES JUST
// COMPUTED, which is what makes this a traverse simulation rather than a
// streaming benchmark: the address of the NEXT HOP cannot be known until the
// current arithmetic finishes, so the hop-to-hop dependency is exposed exactly
// as it is in a real beam search.
//
// WITHIN a hop the addresses ARE all known up front — the whole neighbor row is
// in hand before the first distance is computed — so a kernel may legitimately
// prefetch the candidates it has not reached yet. Only the hop-to-hop step is
// data-dependent. An earlier version of this comment said nothing prefetches,
// which was wrong in the half that matters to anyone writing a gather.
//
// READ THIS SIMULATION AS A PESSIMISTIC ADVERSARIAL MODEL, NOT AS FIDELITY. A
// real beam search keeps a candidate heap and a visited set, revisits nodes, and
// expands in a far more locality-friendly order than argmax-of-the-last-hop; and
// it does other work between hops that overlaps this memory traffic. This walk
// deliberately does none of that. It is a lower bound on cache friendliness,
// chosen so a kernel that looks good here is not relying on locality a real
// query may not supply — not a claim about what a production traversal costs.
func runTraverseSim(a arm, c *traverseCorpus, hops int, scratch []float32) int {
	return runTraverseSimN(a, c, hops, mMax0, scratch)
}

// runTraverseSimN is runTraverseSim with an explicit candidate count, so the
// id-remainder cell can price a list length the four-row grouping does not
// divide. Panics rather than truncating: a count above what the corpus carries
// would silently read another node's neighbors.
func runTraverseSimN(a arm, c *traverseCorpus, hops, ids int, scratch []float32) int {
	if ids <= 0 || ids > c.perNode {
		panic("veckernel: traverse sim asked for an id count the corpus does not carry")
	}
	cur := uint32(0)
	for range hops {
		row := c.neighbors[int(cur)*c.perNode : int(cur)*c.perNode+ids]
		query := c.block[int(cur)*c.dim : int(cur)*c.dim+c.dim]

		a.gather(scratch, query, c.block, c.dim, row)

		best, bestScore := row[0], scratch[0]
		for i := 1; i < ids; i++ {
			if scratch[i] > bestScore {
				best, bestScore = row[i], scratch[i]
			}
		}
		cur = best
	}
	return hops * ids
}

// BenchmarkTraverseSim is the shape the pinned floors are set from.
//
// ALWAYS RUN IT WITH AN EXPLICIT -benchtime=Nx, and compare only numbers taken
// at the SAME N. The result depends on how many hops the walk performs: a random
// walk over this corpus revisits nodes, so a longer walk has warmed more of its
// own working set and every subsequent distance looks cheaper. Measured at
// authoring, same machine and same kernel: NEON at dim 256 reports 23.9
// ns/distance at -benchtime=3000x and 9.9 ns/distance when the framework scaled
// the walk into six figures. Neither is wrong; they measure different amounts of
// cache warmth.
//
// The floor gate in perf_test.go does not use this benchmark for that reason —
// it pins the hop count itself. This one is for A/B work on a kernel change,
// where holding N fixed across the two runs is what makes the comparison mean
// anything.
func BenchmarkTraverseSim(b *testing.B) {
	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			b.Run(fmt.Sprintf("%s/dim=%d", a.name, dim), func(b *testing.B) {
				c := buildTraverseCorpus(dim)
				scratch := make([]float32, mMax0)

				// One hop per iteration keeps the reported ns/op interpretable
				// and lets the framework choose the iteration count.
				b.ResetTimer()
				n := runTraverseSim(a, c, b.N, scratch)
				b.StopTimer()

				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(n), "ns/distance")
				reportPin(b, a.name, dim, func(p Pin) float64 { return p.TraverseNsPerDistance })
			})
		}
	}
}

// --- microbenchmarks --------------------------------------------------------

// BenchmarkDotMicro is the cache-hot single-pair dot: pure arithmetic
// throughput with the memory system removed.
//
// It is reported alongside the traverse number and never instead of it. A
// microbenchmark on two cache-resident vectors flatters every kernel by
// deleting the cache misses that dominate a real traversal, and a kernel tuned
// against this number alone will be tuned against the wrong bottleneck.
func BenchmarkDotMicro(b *testing.B) {
	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			b.Run(fmt.Sprintf("%s/dim=%d", a.name, dim), func(b *testing.B) {
				x, y := seededPair(uint64(dim), dim)
				var sink float32
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					sink += a.dot(x, y)
				}
				b.StopTimer()
				runtime.KeepAlive(sink)

				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N), "ns/distance")
				reportPin(b, a.name, dim, func(p Pin) float64 { return p.MicroNsPerDistance })
			})
		}
	}
}

// BenchmarkGatherMicro is a full 64-candidate hop over a SMALL block: the same
// batch kernel the traverse simulation drives, with DRAM taken out.
//
// IT IS NOT "CACHE-HOT" AT PRODUCTION WIDTHS, and pretending otherwise would
// misattribute the numbers it produces. One hop touches 64 candidates times dim
// times four bytes: 64 KiB at dim 256, 128 KiB at dim 512, 256 KiB at dim 1024,
// 512 KiB at dim 2048. That is larger than any per-core L1 data cache at the
// wider widths — the M4 Max this was authored on has ~128 KiB, a Sapphire Rapids
// core has 48 KiB — so a hop stops fitting in L1 NO MATTER how small the block
// is, and WHERE the cliff falls is a property of the machine rather than of this
// benchmark. Measured on the M4 Max at authoring: 22.5 and 28.5 ns/distance at
// dims 256 and 512, then 121.2 and 224.3 at dims 1024 and 2048, a 4.25x jump
// across a 2x width. Read the cliff's POSITION off the machine you ran on.
//
// That cliff is a property of the WORKLOAD, not of this benchmark, and it is
// the same reason the binary index is fast: its whole working set is ~1 MiB
// where the float index's is ~128 MiB. Read this benchmark as "the hop with DRAM
// removed", never as "the kernel's arithmetic ceiling".
func BenchmarkGatherMicro(b *testing.B) {
	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			b.Run(fmt.Sprintf("%s/dim=%d", a.name, dim), func(b *testing.B) {
				// Small enough to keep the BLOCK out of DRAM at every dim; the
				// candidate set still exceeds L1 above dim 512, see above.
				const rows = 256
				block, query := seededBlock(uint64(dim), rows, dim)
				ids := scatteredIDs(uint64(dim)+1, mMax0, rows)
				dst := make([]float32, mMax0)

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					a.gather(dst, query, block, dim, ids)
				}
				b.StopTimer()

				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*mMax0), "ns/distance")
			})
		}
	}
}

// BenchmarkPublicAPIDispatchOverhead prices the indirect call the dispatcher
// adds, so the cost of the always-assert-the-tier design is a measured number
// rather than an assumption.
//
// Expected to be negligible on the gather path (one indirect call per 64
// distances) and visible-but-small on the scalar path (one per distance). If it
// ever stops being small, THAT is the argument for compile-time specialisation —
// and it should be made with this number in hand.
func BenchmarkPublicAPIDispatchOverhead(b *testing.B) {
	for _, dim := range []int{256, 2048} {
		x, y := seededPair(uint64(dim), dim)

		b.Run(fmt.Sprintf("direct/dim=%d", dim), func(b *testing.B) {
			a := testArms()[0]
			var sink float32
			for i := 0; i < b.N; i++ {
				sink += a.dot(x, y)
			}
			runtime.KeepAlive(sink)
		})
		b.Run(fmt.Sprintf("via-DotF32/dim=%d", dim), func(b *testing.B) {
			var sink float32
			for i := 0; i < b.N; i++ {
				sink += DotF32(x, y)
			}
			runtime.KeepAlive(sink)
		})
	}
}

// reportPin annotates a benchmark with its pinned floor, or says plainly that
// there is not one.
//
// A missing pin is REPORTED, never silently treated as passing. An unmeasured
// slot that reads as blank is how an empty performance contract survives being
// looked at.
func reportPin(b *testing.B, tier string, dim int, pick func(Pin) float64) {
	b.Helper()
	class := machineClass()
	p, ok := pinFor(class, tier, dim)
	switch {
	case !ok:
		b.Logf("PIN: none for class %s/%s/dim=%d", class, tier, dim)
	case p.Unmeasured:
		b.Logf("PIN: UNMEASURED for class %s/%s/dim=%d — awaiting a run on that hardware", class, tier, dim)
	case pick(p) == 0:
		b.Logf("PIN: not set for this benchmark shape (class %s/%s/dim=%d)", class, tier, dim)
	default:
		b.Logf("PIN: %.1f ns/distance on %s (fail threshold %.1f at this cell's own %.2fx "+
			"tolerance, derived from its %.2fx harvest spread)",
			pick(p), p.Machine, pick(p)*p.Tolerance, p.Tolerance, p.SpreadRatio)
	}
}
