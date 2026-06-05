// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	mathrand "math/rand/v2"
	"sort"
)

// build_serial.go is the production twin of the deterministic serial build path
// proven byte-reproducible by build_bench_test.go (newFixedSeedGraph + sortedByID
// + buildSerialDeterministic) and guarded by TestDeterministicSerialIsReproducible.
// The segment_rebuild Format variant (NewDeterministic) builds through this so a
// re-run over an unchanged node set yields byte-identical segments.

// detSeedHi / detSeedLo pin the fixed PCG seed for the deterministic serial
// build — the ONLY behavioural delta vs newRand()'s crypto/rand seed. Same
// constants the measurement benchmark uses (build_bench_test.go fixedSeedHi/Lo).
const (
	detSeedHi = 0xdeadbeef
	detSeedLo = 0xcafebabe
)

// buildBinaryHNSWSerialDeterministic builds a binary HNSW graph deterministically:
// a FIXED PCG seed (so level assignment is reproducible) and SERIAL insertion in
// stable sorted-by-id order (so neighbor lists are reproducible — the concurrent
// builder's goroutine interleaving is the other non-determinism source this path
// eliminates). Encode (serial.go) is already deterministic, so identical items in
// → byte-identical encoded blob out.
//
// It is the serial counterpart to buildBinaryHNSWParallel: same params, same
// (*binaryGraph).Insert algorithm, only the seed + insertion concurrency differ.
// Determinism is a PER-SEGMENT property; cross-segment build concurrency is the
// engine/Manager layer's job (each goroutine builds one segment via this fn).
func buildBinaryHNSWSerialDeterministic(items []binaryBuildItem, vecBytes, m, efConstruction int) *binaryGraph {
	g := newBinaryGraph(vecBytes, m, efConstruction)
	// Override the crypto-seeded rng with a fixed PCG seed — the only delta vs
	// the default newBinaryGraph construction (newRand at internals.go).
	g.rng = mathrand.New(mathrand.NewPCG(detSeedHi, detSeedLo))

	// Insert in a STABLE sorted-by-id order so the serial insertion sequence is
	// reproducible across runs (a local copy + sort, leaving items untouched).
	sorted := make([]binaryBuildItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

	for _, it := range sorted {
		g.Insert(it.id, it.vec)
	}
	return g
}
