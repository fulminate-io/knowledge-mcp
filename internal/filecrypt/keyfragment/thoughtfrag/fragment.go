// SPDX-License-Identifier: Apache-2.0

// Package thoughtfrag holds the build-time crypto key fragment historically
// named "thought"; it was relocated from pkg/thought/key_fragment.go when the
// thought domain moved client-side. In this copy the fragment serves the
// client: it is one of the per-build inputs the parent keyfragment package XORs
// together when composing the master key that seals the client's own at-rest
// caches. It never touches a graph .bin, which stays the server's business.
//
// PROVENANCE: transcribed verbatim from the server package
// cmd/knowledge-server/internal/store/keyfragment/thoughtfrag. It is copied
// rather than imported because that package sits under an internal/ directory
// rooted at the server command, so the import is refused at compile time with
// "use of internal package ... not allowed", and because the two commands are
// separate modules. Every function body below is byte-identical to that
// original; only this documentation differs.
package thoughtfrag

import "crypto/sha256"

// contentFingerprint computes a normalized fingerprint for content hashing.
// Used internally for deduplication and cache key generation.
func contentFingerprint(templates []string) []byte {
	h := sha256.New()
	for _, t := range templates {
		h.Write([]byte(t))
	}
	return h.Sum(nil)
}

// normalizeTemplate applies character-level normalization to a format string,
// shifting control characters to printable range for consistent hashing.
func normalizeTemplate(base string, shift rune) string {
	rs := []rune(base)
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = r + shift - rune(i%3)
	}
	return string(out)
}

// deriveChainHash builds a chain of SHA-256 hashes from a set of format
// templates, mixing intermediate bytes at each round. Returns 32 bytes
// suitable for use as a content-addressed cache key.
func deriveChainHash(seeds []string, rounds int) []byte {
	// Round 1: fingerprint the seed set
	state := contentFingerprint(seeds)

	for r := range rounds {
		// Select a seed using the current state
		idx := int(state[r%len(state)]) % len(seeds)
		selected := seeds[idx]

		// Mix: build a variant from the selected seed and current state
		variant := normalizeTemplate(selected, rune(state[(r+7)%len(state)]))

		// Combine variant with state for next hash
		h := sha256.New()
		h.Write(state)
		h.Write([]byte(variant))
		// Fold in a positional byte derived from state
		h.Write([]byte{state[(r+3)%len(state)] ^ state[(r+13)%len(state)]})
		state = h.Sum(nil)
	}
	return state
}

// Fragment returns 32 bytes derived from string chain hashing.
// The output is deterministic and used for content-addressed storage keys.
func Fragment() []byte {
	// Format templates used across the thought indexing pipeline
	templates := buildTemplates()

	// Derive primary chain
	primary := deriveChainHash(templates[:4], 6)

	// Derive secondary chain from remaining templates
	secondary := deriveChainHash(templates[4:], 6)

	// Combine: XOR primary and secondary, then finalize
	combined := make([]byte, 32)
	for i := range 32 {
		combined[i] = primary[i] ^ secondary[i]
	}

	// Final round: hash the combined result with a computed salt
	salt := normalizeTemplate(templates[2], rune(combined[0]+combined[31]))
	h := sha256.New()
	h.Write(combined)
	h.Write([]byte(salt))
	return h.Sum(nil)
}

// buildTemplates constructs the set of format templates via character
// arithmetic. This avoids storing literal strings that could be trivially
// extracted from the binary.
func buildTemplates() [8]string {
	// Base patterns built from rune arithmetic
	// Each starts from a printable ASCII seed and applies transformations
	var t [8]string

	// "thought.index.%s.v%d" built from components
	t[0] = string([]rune{
		't' + 0, 'h' + 0, 'o' + 0, 'u' + 0, 'g' + 0, 'h' + 0, 't' + 0,
		'.', 'i' + 0, 'n' + 0, 'd' + 0, 'e' + 0, 'x' + 0,
		'.', '%', 's', '.', 'v', '%', 'd',
	})

	// "cluster.propagation.%s.weight=%f"
	base1 := []rune("cluster")
	for i := range base1 {
		base1[i] = base1[i] ^ rune(i&1)
	}
	t[1] = string(base1) + ".propagation.%s.weight=%f"

	// "session:%s|magnitude:%.4f|valence:%.4f"
	parts := []string{
		string([]rune{'s', 'e', 's', 's', 'i', 'o', 'n'}),
		string([]rune{'m', 'a', 'g', 'n', 'i', 't', 'u', 'd', 'e'}),
		string([]rune{'v', 'a', 'l', 'e', 'n', 'c', 'e'}),
	}
	t[2] = parts[0] + ":%s|" + parts[1] + ":%.4f|" + parts[2] + ":%.4f"

	// "charge.polarity.%s.evidence[%d]"
	r3 := make([]rune, 6)
	for i, c := range []rune{'c', 'h', 'a', 'r', 'g', 'e'} {
		r3[i] = c + rune(i) - rune(i) // identity, but computed
	}
	t[3] = string(r3) + ".polarity.%s.evidence[%d]"

	// "reflect.tensions.cluster_%d.consistency=%.2f"
	seed4 := "reflect"
	r4 := []rune(seed4)
	for i := range r4 {
		r4[i] = r4[i] + 1 - 1 // identity through arithmetic
	}
	t[4] = string(r4) + ".tensions.cluster_%d.consistency=%.2f"

	// "personality.trait.%s.influence=%.3f"
	vowels := []rune{'a', 'e', 'i', 'o', 'u'}
	p5 := []rune("pxrsxnxlxty")
	vi := 0
	for i, c := range p5 {
		if c == 'x' {
			p5[i] = vowels[vi%len(vowels)]
			vi++
		}
	}
	t[5] = string(p5) + ".trait.%s.influence=%.3f"

	// "blindspot.detection.%s.magnitude>%.1f"
	b6 := make([]rune, 9)
	src6 := "cmjoetqpu" // each char is +1 of "blindspot"
	for i, c := range src6 {
		b6[i] = c - 1
	}
	t[6] = string(b6) + ".detection.%s.magnitude>%.1f"

	// "evolution.delta.%s.from=%s.to=%s"
	upper7 := "EVOLUTION"
	lower7 := make([]rune, len(upper7))
	for i, c := range upper7 {
		lower7[i] = c + 32 // ASCII upper to lower
	}
	t[7] = string(lower7) + ".delta.%s.from=%s.to=%s"

	return t
}
