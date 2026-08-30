// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"log/slog"
)

// fakeEmbedder is the DETERMINISTIC FAKE ARM: a first-class registered
// provider that derives each vector from a hash of the text at the
// configured width. It ships in the OSS binary — it is not a test double
// — because the end-to-end program needs a provider whose output a test
// can predict exactly, across processes and machines, with no network and
// no key. The in-repo precedent for a fake in a non-test file is the LLM
// package's FakeClient.
//
// Its vectors carry NO SEMANTIC MEANING. Two texts that mean the same
// thing hash to unrelated bytes, so search results from this arm are
// arbitrary. That is the point — determinism, not quality — and it is why
// construction warns.
type fakeEmbedder struct {
	// width is the vector length in BYTES, derived from the configured
	// dimension in bits.
	width int
}

// Compile-time assertion: *fakeEmbedder satisfies BinaryEmbedder. The
// test-local stub this arm promotes lacked one, which let its shape drift
// from the interface silently.
var _ BinaryEmbedder = (*fakeEmbedder)(nil)

func init() { RegisterProvider(ProviderFake, newFakeFromConfig) }

// newFakeFromConfig is the registered factory.
//
// width is cfg.Dimension/8 — 256 bits gives 32 bytes. A zero dimension
// would silently yield width 0 and an EMPTY vector for every text, which
// the pipeline's segment builder skips without a word, so an end-to-end
// run would index nothing with every gate green. That hole is closed one
// layer up: Config.Validate refuses any dimension other than the accepted
// one outright, so a zero-width Config can never reach this constructor.
// The refusal is NOT duplicated here — one refusal, two enforcement
// layers (the TOML parser and Validate), each named where it lives.
func newFakeFromConfig(_ context.Context, cfg *Config) (BinaryEmbedder, error) {
	if cfg == nil {
		return nil, errNilConfig()
	}
	slog.Warn("embed: the DETERMINISTIC FAKE embedder is configured — it produces meaningless hash-derived vectors, and search results from this graph carry no semantic meaning",
		"provider", ProviderFake, "dimension", cfg.Dimension)
	return &fakeEmbedder{width: cfg.Dimension / 8}, nil
}

// Available always reports true: the fake needs no credential and no
// network, so there is no state in which it is unavailable.
func (e *fakeEmbedder) Available() bool { return true }

// EmbedBinary returns the deterministic vector for one text.
func (e *fakeEmbedder) EmbedBinary(_ context.Context, text string) ([]byte, error) {
	return e.vector(text), nil
}

// EmbedBinaryBatch maps the derivation over texts. The output slice is
// pre-sized the way the HTTP arms pre-size theirs. No goroutine per item:
// at end-to-end corpus sizes one sha256 per text is not the bottleneck and
// the scheduling would cost more than it saves; concurrency lives above
// this layer in the pipeline.
func (e *fakeEmbedder) EmbedBinaryBatch(_ context.Context, texts []string) ([][]byte, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]byte, len(texts))
	for i, t := range texts {
		out[i] = e.vector(t)
	}
	return out, nil
}

// vector derives width bytes from text. It is a PURE function of the text
// and the width — same text, same width, same bytes, in every process on
// every machine — which is what lets an end-to-end test assert pipeline
// output against a value it computes itself.
//
// For width <= 32 the first width bytes of sha256(text) are used. Beyond
// that the digest is extended by re-hashing it with a little-endian
// counter suffix, so the derivation stays deterministic and total at any
// width the dtype gate ever admits rather than truncating or repeating.
func (e *fakeEmbedder) vector(text string) []byte {
	if e.width <= 0 {
		return nil
	}
	seed := sha256.Sum256([]byte(text))
	if e.width <= len(seed) {
		out := make([]byte, e.width)
		copy(out, seed[:])
		return out
	}
	out := make([]byte, 0, e.width)
	out = append(out, seed[:]...)
	var counter uint64
	var suffix [8]byte
	for len(out) < e.width {
		binary.LittleEndian.PutUint64(suffix[:], counter)
		block := sha256.Sum256(append(seed[:], suffix[:]...))
		out = append(out, block[:]...)
		counter++
	}
	return out[:e.width]
}
