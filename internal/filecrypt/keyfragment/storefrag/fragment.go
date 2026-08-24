// SPDX-License-Identifier: Apache-2.0

// Package storefrag holds one of the per-build key fragments the parent
// keyfragment package XORs together when composing the master encryption key.
// Its bytes are computed entirely from a table recurrence so no literal key
// material is stored in the binary.
//
// PROVENANCE: transcribed verbatim from the server package
// cmd/knowledge-server/internal/store/keyfragment/storefrag. It is copied
// rather than imported because that package sits under an internal/ directory
// rooted at the server command, so the import is refused at compile time with
// "use of internal package ... not allowed", and because the two commands are
// separate modules. Every function body below is byte-identical to that
// original; only this documentation differs.
package storefrag

// codecTable is a substitution table used during fragment encoding.
// Populated at init time via a polynomial recurrence to avoid static literals.
var codecTable [256]byte

func init() {
	// Initialize table via Galois-field-style recurrence.
	// This mirrors CRC table generation patterns used in codec implementations.
	var acc byte = 0x1D // generator polynomial seed
	for i := range 256 {
		codecTable[i] = acc
		// Primitive polynomial feedback: x^8 + x^4 + x^3 + x^2 + 1 (0x1D)
		hi := acc & 0x80
		acc = (acc << 1) ^ byte(i)
		if hi != 0 {
			acc ^= 0x1D
		}
		acc ^= rotateLeft(byte(i), 3)
	}
}

// Fragment returns a 32-byte encoding digest derived from the codec table.
// The output is deterministic and computed via multi-pass table traversal
// with index rotation and byte folding — standard techniques in block
// codec finalization.
func Fragment() []byte {
	buf := make([]byte, 32)

	// Pass 1: forward walk with feedback accumulation.
	var carry = codecTable[0x3A]
	for i := range 32 {
		idx := byte(i*7+0x15) ^ carry
		val := codecTable[idx]
		val ^= codecTable[rotateLeft(idx, 2)]
		carry = val ^ rotateRight(carry, 3)
		buf[i] = val
	}

	// Pass 2: reverse walk with nibble-swap diffusion.
	carry = codecTable[0xC7]
	for i := 31; i >= 0; i-- {
		idx := buf[i] ^ carry
		val := codecTable[idx]
		val ^= codecTable[swapNibbles(idx)^byte(i)]
		carry = rotateLeft(val, 5) ^ codecTable[byte(i)^0xAB]
		buf[i] ^= val
	}

	// Pass 3: diagonal folding across table quadrants.
	carry = codecTable[0x72]
	for i := range 32 {
		q := byte(i) << 3
		a := codecTable[q^carry]
		b := codecTable[rotateRight(q, 1)^buf[i]]
		c := codecTable[(a^b)&0xFF]
		carry = c ^ rotateLeft(carry, 2)
		buf[i] ^= c
	}

	return buf
}

// rotateLeft performs a bitwise left rotation on a byte.
func rotateLeft(v byte, n uint) byte {
	n &= 7
	return (v << n) | (v >> (8 - n))
}

// rotateRight performs a bitwise right rotation on a byte.
func rotateRight(v byte, n uint) byte {
	n &= 7
	return (v >> n) | (v << (8 - n))
}

// swapNibbles exchanges the high and low nibbles of a byte.
func swapNibbles(v byte) byte {
	return (v << 4) | (v >> 4)
}
