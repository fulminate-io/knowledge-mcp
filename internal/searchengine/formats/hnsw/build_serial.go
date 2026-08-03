// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	mathrand "math/rand/v2"
	"sort"
)

// build_serial.go is the HNSW build path — byte-reproducible serial construction,
// the ONLY builder. Format.Build runs through it for every segment (embed ship and
// segment_rebuild alike), so a re-run over an unchanged node set yields
// byte-identical segments. Reproducibility is guarded by
// TestDeterministicSerialIsReproducible.

// detSeedHi / detSeedLo pin the fixed PCG seed for the deterministic serial
// build — the ONLY behavioural delta vs newRand()'s crypto/rand seed.
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
// Determinism is a PER-SEGMENT property; cross-segment build concurrency is the
// engine/Manager layer's job (each goroutine builds one segment via this fn).
func buildBinaryHNSWSerialDeterministic(items []binaryBuildItem, vecBytes, m, efConstruction int) *binaryGraph {
	g := newBinaryGraph(vecBytes, m, efConstruction)
	// Override the crypto-seeded rng with a fixed PCG seed — the only delta vs
	// the default newBinaryGraph construction (newRand at internals.go).
	g.rng = mathrand.New(mathrand.NewPCG(detSeedHi, detSeedLo))

	// Insert in a STABLE sorted-by-id order so the serial insertion sequence is
	// reproducible across runs (a local copy + sort, leaving items untouched).
	//
	// SliceStable, not Slice: sort.Slice is an unstable quicksort, so two items sharing
	// an id could come out in either order depending on the input permutation. Callers
	// deduplicate by id before building, so equal keys should not arrive here at all —
	// but the tie-break is what decides the survivor if one ever does, and a
	// nondeterministic tie-break would silently reintroduce run-to-run variation into a
	// builder whose whole contract is byte reproducibility.
	sorted := make([]binaryBuildItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

	for _, it := range sorted {
		g.Insert(it.id, it.vec)
	}
	return g
}
