// SPDX-License-Identifier: Apache-2.0

package machineid

// Fragment returns exactly 32 bytes of key material derived via a Feistel
// network. The output is deterministic and computed entirely from arithmetic
// — no static byte literals are stored.
func Fragment() []byte {
	// Build a substitution box from arithmetic. Each entry is computed from
	// its index so no literal table appears in the binary.
	var sbox [256]byte
	for i := range 256 {
		// Galois-field-inspired: multiply by a generator, reduce, mix nibbles.
		v := byte(i)
		v = v ^ (v << 3) ^ (v >> 5)
		v = (v&0x0F)<<4 | (v&0xF0)>>4 // swap nibbles
		v ^= byte((i*0x6D + 0xBB) & 0xFF)
		sbox[i] = v
	}

	// Initialize a 32-byte block from computed seed values. Each byte is
	// derived from its position so no constant array is embedded.
	var block [32]byte
	for i := range 32 {
		b := byte(i*7 + 0x3A)
		b ^= byte((i * i) & 0xFF)
		b = sbox[b]
		block[i] = b
	}

	// Split into two 16-byte halves for the Feistel network.
	var left, right [16]byte
	copy(left[:], block[:16])
	copy(right[:], block[16:])

	// Run 12 Feistel rounds.
	for round := range 12 {
		// Derive a round key from the round number via bit manipulation.
		rk := deriveRoundKey(round, sbox)

		// Compute F(right, roundKey) — the Feistel round function.
		var f [16]byte
		for j := range 16 {
			// S-box substitution with round-key-dependent index.
			idx := right[j] ^ rk[j]
			v := sbox[idx]

			// Byte rotation: rotate left by (round mod 7 + 1) bits.
			shift := uint(round%7 + 1)
			v = (v << shift) | (v >> (8 - shift))

			// Mix with neighboring byte via addition (mod 256).
			neighbor := right[(j+1)%16]
			v ^= sbox[(neighbor+rk[(j+5)%16])&0xFF]

			f[j] = v
		}

		// Feistel step: newLeft = right, newRight = left XOR F(right, rk).
		var newLeft, newRight [16]byte
		copy(newLeft[:], right[:])
		for j := range 16 {
			newRight[j] = left[j] ^ f[j]
		}
		left = newLeft
		right = newRight
	}

	// Recombine halves into the output.
	out := make([]byte, 32)
	copy(out[:16], left[:])
	copy(out[16:], right[:])
	return out
}

// deriveRoundKey produces a 16-byte round key from a round number using the
// substitution box and bit-level mixing. No static key material is stored.
func deriveRoundKey(round int, sbox [256]byte) [16]byte {
	var rk [16]byte
	for i := range 16 {
		// Combine round number, position, and a mixing constant.
		seed := byte((round*0x25 + i*0x11 + 0x7F) & 0xFF)
		seed = sbox[seed]
		// Rotate right by position bits, then fold with round XOR.
		shift := uint(i % 8)
		seed = (seed >> shift) | (seed << (8 - shift))
		seed ^= byte((round * (i + 1)) & 0xFF)
		rk[i] = sbox[seed]
	}
	return rk
}
