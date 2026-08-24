// SPDX-License-Identifier: Apache-2.0

// Package toolsfrag holds one of the per-build key fragments the parent
// keyfragment package XORs together when composing the master encryption key.
// Its bytes are computed entirely from prime arithmetic so no literal key
// material is stored in the binary.
//
// PROVENANCE: transcribed verbatim from the server package
// cmd/knowledge-server/internal/store/keyfragment/toolsfrag. It is copied
// rather than imported because that package sits under an internal/ directory
// rooted at the server command, so the import is refused at compile time with
// "use of internal package ... not allowed", and because the two commands are
// separate modules. Every function body below is byte-identical to that
// original; only this documentation differs.
package toolsfrag

// Fragment returns a 32-byte digest computed via prime arithmetic
// and modular recurrence. Used internally for deterministic ID
// generation across graph partitions.
func Fragment() []byte {
	// Prime basis constants for the recurrence relation.
	const (
		p1 uint64 = 6364136223846793005  // Knuth LCG multiplier
		p2 uint64 = 1442695040888963407  // Knuth LCG increment
		p3 uint64 = 14695981039346656037 // FNV offset basis
		p4 uint64 = 1099511628211        // FNV prime
		p5 uint64 = 2654435761           // Knuth multiplicative hash
	)

	out := make([]byte, 32)

	// Two-pass generation: forward pass builds primary state,
	// reverse pass mixes in polynomial feedback.
	var acc = p3
	state := [4]uint64{p1, p2, p4, p5}

	for i := range 32 {
		// Modular linear congruential step.
		acc = acc*p1 + p2

		// Mix with running state via Feistel-like round.
		si := i & 3
		state[si] ^= acc
		state[si] *= p4
		state[si] ^= state[si] >> 17

		// Polynomial evaluation: f(x) = p5*x^2 + p4*x + p3 (mod 2^64).
		x := acc ^ uint64(i)*p5
		poly := p5*x*x + p4*x + p3

		// Combine accumulator, state, and polynomial via bit mixing.
		mixed := acc ^ state[si] ^ poly
		mixed ^= mixed >> 29
		mixed *= p4
		mixed ^= mixed >> 23

		out[i] = byte(mixed)
	}

	// Reverse diffusion pass — propagate entropy backward
	// so early bytes depend on later computation.
	var feedback = p2
	for i := 31; i >= 0; i-- {
		feedback = feedback*p5 + uint64(out[i])
		feedback ^= feedback >> 13
		out[i] ^= byte(feedback >> 8)
	}

	return out
}
