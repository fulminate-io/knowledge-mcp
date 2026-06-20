// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"crypto/rand"
	"encoding/binary"
	mathrand "math/rand/v2"
)

// newRand creates a new PCG random number generator seeded from crypto/rand.
// Falls back to a fixed seed if crypto/rand is unavailable (should never
// happen in practice).
func newRand() *mathrand.Rand {
	var seed [16]byte
	if _, err := rand.Read(seed[:]); err != nil {
		// Extremely unlikely; use a fixed fallback seed.
		return mathrand.New(mathrand.NewPCG(0xdeadbeef, 0xcafebabe))
	}
	s1 := binary.LittleEndian.Uint64(seed[:8])
	s2 := binary.LittleEndian.Uint64(seed[8:])
	return mathrand.New(mathrand.NewPCG(s1, s2))
}

// hnswNode stores a node's metadata and neighbor lists per layer.
type hnswNode struct {
	externalID string     // maps back to Document.ID
	maxLevel   int        // highest layer this node appears in
	neighbors  [][]uint32 // neighbors[layer] = []uint32 of internal IDs
}

// binaryBuildItem holds a binary vector with its external ID for batch
// insertion by the deterministic serial builder.
type binaryBuildItem struct {
	id  string
	vec []byte
}
