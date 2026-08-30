// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"math"
	"math/rand/v2"
	"runtime"
	"testing"
	"time"
)

// stride_test.go is a MEASUREMENT CELL, not a gate. It asks one question: does
// the corpus's power-of-two row stride cost anything to cache set conflicts?
//
// THE HYPOTHESIS. Every production width is a power of two times four bytes —
// dim 1024 is a 4096-byte row, dim 2048 an 8192-byte row. A set-associative cache
// indexes with low address bits, so rows separated by an exact power of two land
// in the SAME set as each other. A hop touches 64 candidate rows; if all 64 map
// to one set of an 8- or 12-way cache, 56 of them evict each other before use,
// and the kernel pays misses that a one-line offset would have avoided entirely.
//
// THE PROBE. Same walk, same kernel, same candidate count — only the distance
// BETWEEN rows changes, from exactly dim*4 bytes to dim*4 + 64. If aliasing is
// real the padded corpus is measurably faster; if it is not, the padding costs a
// little extra footprint and nothing else.
//
// WHAT A POSITIVE RESULT WOULD MEAN, and why this file changes nothing on its
// own: row stride is a property of the INDEX FORMAT, not of this package. The
// kernels take row pointers and never compute a stride. So a real effect here is
// a finding to escalate to whoever owns the segment layout, not a license for the
// lab to start padding anything.

// stridePadFloats is the padding under test: 64 bytes, one amd64 cache line.
//
// One line rather than more, because the hypothesis is specifically about set
// index collisions and a single line is enough to walk every row onto a different
// set. Larger padding would confound the question with plain footprint growth.
const stridePadFloats = 16

// strideCorpus is a traverse corpus whose rows are spaced by an arbitrary stride
// rather than packed at exactly dim floats apart.
type strideCorpus struct {
	block     []float32
	dim       int
	stride    int
	nodes     int
	neighbors []uint32
}

// buildStrideCorpus builds a corpus of EXACTLY nodes rows at the given stride.
//
// NODE COUNT IS THE CONTROLLED VARIABLE, not byte budget, and that is the whole
// methodology. Sizing both layouts to the same number of BYTES gives them
// different node counts, which gives them different random walks over different
// working sets — so the comparison would measure two different workloads and
// attribute the difference to stride. Holding the node count and the neighbor
// seed fixed makes the two runs visit the SAME node indices in the same order;
// the only thing that differs is how far apart those rows sit in memory, which is
// the question. The padded corpus is correspondingly a few percent larger in
// bytes, and that residual is named rather than hidden.
func buildStrideCorpus(dim, stride, nodes int) *strideCorpus {
	// TWO INDEPENDENT RANDOM SOURCES, and this is not tidiness — a single source
	// silently broke this probe. The block fill consumes one draw per float, so
	// a packed and a padded corpus of the same node count consume DIFFERENT
	// numbers of draws before reaching the neighbor list. Sharing a source
	// therefore gives the two layouts different neighbor graphs and different
	// walks, and the probe reports the difference between two workloads as if it
	// were the effect of row spacing. It read +366% at dim 512 that way.
	//
	// The neighbor source is seeded from dim alone, so both layouts at a given
	// width get byte-identical neighbor lists and visit the same nodes in the
	// same order.
	// ROW CONTENTS MUST BE STRIDE-INVARIANT, and this is the second confound this
	// probe had to shed. Filling the block as one stream makes row i's DATA
	// depend on the stride — the padding floats shift every later row's draws —
	// so the two layouts score differently, pick different argmaxes, and walk
	// different paths. Both tiers then move together at a given width, which is
	// the tell that the corpus changed rather than the cache behavior.
	//
	// Seeding per ROW makes row i byte-identical at any stride. Scores are then
	// identical, the walk is identical, and the ONLY difference between the two
	// runs is how far apart the rows sit in memory — which is the question.
	block := make([]float32, nodes*stride)
	for row := range nodes {
		rr := rand.New(rand.NewPCG(0x5eed, uint64(dim)*1_000_003+uint64(row)))
		base := row * stride
		for i := range dim {
			block[base+i] = rr.Float32()*2 - 1
		}
	}

	neighborRand := rand.New(rand.NewPCG(0xa11a5, uint64(dim)))
	neighbors := make([]uint32, nodes*mMax0)
	for i := range neighbors {
		neighbors[i] = uint32(neighborRand.IntN(nodes))
	}
	return &strideCorpus{block: block, dim: dim, stride: stride, nodes: nodes, neighbors: neighbors}
}

// walkStride runs the same argmax walk over a strided corpus.
//
// It scores rows one at a time through the tier's SCALAR dot rather than the
// fused gather, because the gather's ABI assumes rows are exactly dim apart and
// this probe exists precisely to break that assumption. Both arms of the
// comparison use the same path, so the comparison stays honest even though the
// absolute numbers are not comparable with the pinned cells.
func walkStride(a arm, c *strideCorpus, hops int) float64 {
	scratch := make([]float32, mMax0)
	run := func() int {
		cur := uint32(0)
		for range hops {
			row := c.neighbors[int(cur)*mMax0 : int(cur)*mMax0+mMax0]
			q := c.block[int(cur)*c.stride : int(cur)*c.stride+c.dim]
			for i, id := range row {
				off := int(id) * c.stride
				scratch[i] = a.dot(q, c.block[off:off+c.dim])
			}
			best, bestScore := row[0], scratch[0]
			for i := 1; i < mMax0; i++ {
				if scratch[i] > bestScore {
					best, bestScore = row[i], scratch[i]
				}
			}
			cur = best
		}
		return hops * mMax0
	}
	run()
	best := math.Inf(1)
	for range measureRuns {
		start := time.Now()
		n := run()
		if v := float64(time.Since(start).Nanoseconds()) / float64(n); v < best {
			best = v
		}
	}
	return best
}

// strideTerminal returns where a strided walk ends, so the probe can prove the
// two layouts are the same workload before comparing their timings.
func strideTerminal(a arm, c *strideCorpus, hops int) uint32 {
	scratch := make([]float32, mMax0)
	cur := uint32(0)
	for range hops {
		row := c.neighbors[int(cur)*mMax0 : int(cur)*mMax0+mMax0]
		q := c.block[int(cur)*c.stride : int(cur)*c.stride+c.dim]
		for i, id := range row {
			off := int(id) * c.stride
			scratch[i] = a.dot(q, c.block[off:off+c.dim])
		}
		best, bestScore := row[0], scratch[0]
		for i := 1; i < mMax0; i++ {
			if scratch[i] > bestScore {
				best, bestScore = row[i], scratch[i]
			}
		}
		cur = best
	}
	return cur
}

// TestRowStrideAliasingProbe measures packed against padded row strides.
//
// Env-gated with the harvester, because it is minutes of timing work and belongs
// with the other deliberate measurements rather than in the ordinary suite.
func TestRowStrideAliasingProbe(t *testing.T) {
	if !envEnabled(HarvestEnv) {
		t.Skipf("STRIDE PROBE NOT RUN. Set %s=1 to measure this host. Without it the question "+
			"of whether power-of-two row strides cost cache conflicts goes unasked here.", HarvestEnv)
	}
	size, err := corpusBytes()
	if err != nil {
		t.Fatalf("%v", err)
	}

	t.Logf("STRIDE PROBE on %s/%s, class %q, ~%d MiB packed, %d hops, best of %d. Node count and "+
		"neighbor seed are held FIXED between the two layouts, so both walks visit the same "+
		"nodes in the same order and only the byte spacing differs; the padded corpus is "+
		"%.1f%% larger in bytes as a result.",
		runtime.GOOS, runtime.GOARCH, machineClass(), size>>20, perfHops, measureRuns,
		100*float64(stridePadFloats)/float64(pinnedDims[0]))

	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			nodes := size / (dim * 4)
			packed := buildStrideCorpus(dim, dim, nodes)
			padded := buildStrideCorpus(dim, dim+stridePadFloats, nodes)

			// THE CONTROL, ASSERTED RATHER THAN ASSUMED. If the two layouts do
			// not walk to the same node they are not the same workload, and any
			// difference between them is unattributable. This probe has already
			// been wrong twice in exactly that way.
			if e1, e2 := strideTerminal(a, packed, perfHops), strideTerminal(a, padded, perfHops); e1 != e2 {
				t.Errorf("dim=%d %s: packed walk ends at %d, padded at %d — the layouts are not "+
					"running the same workload, so no timing comparison between them means "+
					"anything", dim, a.name, e1, e2)
				continue
			}

			p := walkStride(a, packed, perfHops)
			q := walkStride(a, padded, perfHops)
			delta := 100 * (q - p) / p

			verdict := "no aliasing effect"
			switch {
			case delta <= -5:
				verdict = "PADDING WINS — possible set-conflict aliasing, ESCALATE (index-format question)"
			case delta >= 5:
				verdict = "padding is worse (extra footprint, no conflict to relieve)"
			}
			t.Logf("STRIDE  %-14s dim=%-5d packed %7.1f  padded(+%dB) %7.1f  %+.1f%%  %s",
				a.name, dim, p, stridePadFloats*4, q, delta, verdict)
		}
	}
}
