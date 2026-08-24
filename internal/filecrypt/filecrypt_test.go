// SPDX-License-Identifier: Apache-2.0

package filecrypt

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment/storefrag"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment/thoughtfrag"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment/toolsfrag"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/machineid"
)

// plaintextSentinel is 95 printable characters. The length is chosen so a
// literal `strings -n 60` sweep over an artifact would report it, which is what
// makes the no-plaintext assertions below no weaker than the shell probe an
// operator would run by hand.
const plaintextSentinel = "SENTINEL-a-recognizable-run-of-printable-characters-that-any-strings-sweep-would-surface-000000"

func TestFileCrypt_SealOpenRoundTrip(t *testing.T) {
	const path = "/tmp/filecrypt-roundtrip/record.bin"
	want := []byte("the quick brown fox jumps over the lazy dog")

	blob, err := Seal(path, want)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(path, blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round trip changed the payload: got %q want %q", got, want)
	}
}

// TestFileCrypt_WrongPathFailsAuth proves the file path is bound INTO the key
// rather than merely passed alongside it. Without this test a derivation that
// ignored its salt would satisfy every other assertion in this file.
func TestFileCrypt_WrongPathFailsAuth(t *testing.T) {
	const pathA = "/tmp/filecrypt-paths/a.bin"
	const pathB = "/tmp/filecrypt-paths/b.bin"
	payload := []byte("bound to a single path")

	blob, err := Seal(pathA, payload)
	if err != nil {
		t.Fatalf("Seal at pathA: %v", err)
	}

	// Known-positive control, same run: the blob DOES open at the path it was
	// sealed for, so the failure below is the path binding and not a broken seal.
	if _, err := Open(pathA, blob); err != nil {
		t.Fatalf("control: Open at the sealing path should succeed: %v", err)
	}

	if _, err := Open(pathB, blob); err == nil {
		t.Fatal("Open at a different path succeeded; the path is not bound into the key")
	}
}

// TestFileCrypt_MasterKeyIsMachineBound asserts the PROPERTY — a different
// machine identity yields a different master key, the same one yields the same
// key — rather than asserting this host's particular configuration.
func TestFileCrypt_MasterKeyIsMachineBound(t *testing.T) {
	frags := []keyfragment.KeyFragment{
		storefrag.Fragment,
		toolsfrag.Fragment,
		thoughtfrag.Fragment,
		machineid.Fragment,
		keyfragment.Fragment,
	}

	first := keyfragment.DeriveMasterKey(frags, "1111222233334444")
	again := keyfragment.DeriveMasterKey(frags, "1111222233334444")
	other := keyfragment.DeriveMasterKey(frags, "aaaabbbbccccdddd")

	if len(first) != 32 {
		t.Fatalf("master key length = %d, want 32", len(first))
	}
	if !bytes.Equal(first, again) {
		t.Fatal("the same machine identity produced two different master keys")
	}
	if bytes.Equal(first, other) {
		t.Fatal("a different machine identity produced the same master key; the key is not machine-bound")
	}
}

// TestFileCrypt_SealedBytesCarryNoPlaintext is the at-rest confidentiality
// assertion. Leg (a) asserts an ABSENCE, so leg (b) supplies the known-positive
// control in the same run: the very same sentinel IS present in the unsealed
// input. Without the control, a fixture that never carried the sentinel would
// satisfy leg (a) just as well as a working seal.
func TestFileCrypt_SealedBytesCarryNoPlaintext(t *testing.T) {
	const path = "/tmp/filecrypt-confidentiality/record.bin"
	payload := []byte("prefix|" + plaintextSentinel + "|suffix")

	// (b) Known-positive control: the sentinel is in the plaintext input.
	if !bytes.Contains(payload, []byte(plaintextSentinel)) {
		t.Fatal("control: the sentinel is not in the plaintext input, so the absence check below would be vacuous")
	}

	// (a) The sealed blob carries no trace of it.
	blob, err := Seal(path, payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(blob, []byte(plaintextSentinel)) {
		t.Fatal("the sealed blob contains the plaintext sentinel")
	}
}

// TestFileCrypt_FragmentsAreDistinctAndStable is the transcription catcher. A
// copy-paste that leaves two packages returning the same bytes, or a body
// truncated to a short or all-zero slice, fails here and nowhere else.
//
// THE HONEST LIMIT OF THIS TEST, stated so nobody reads more into it: it cannot
// assert byte-EXACT fidelity to the server's fragments, because the server
// package is under an internal/ directory this module may not import. It does
// not need to. A transcription typo yields a different but equally machine-bound
// key, and nothing on this side reads a file the server wrote.
func TestFileCrypt_FragmentsAreDistinctAndStable(t *testing.T) {
	named := []struct {
		name string
		fn   keyfragment.KeyFragment
	}{
		{"storefrag", storefrag.Fragment},
		{"toolsfrag", toolsfrag.Fragment},
		{"thoughtfrag", thoughtfrag.Fragment},
		{"machineid", machineid.Fragment},
		{"keyfragment", keyfragment.Fragment},
	}

	seen := make(map[string]string, len(named))
	for _, f := range named {
		got := f.fn()
		if len(got) != 32 {
			t.Fatalf("%s.Fragment() length = %d, want 32", f.name, len(got))
		}
		if bytes.Equal(got, make([]byte, 32)) {
			t.Fatalf("%s.Fragment() returned 32 zero bytes", f.name)
		}
		if prev, dup := seen[string(got)]; dup {
			t.Fatalf("%s.Fragment() returned the same bytes as %s.Fragment()", f.name, prev)
		}
		seen[string(got)] = f.name

		if again := f.fn(); !bytes.Equal(got, again) {
			t.Fatalf("%s.Fragment() is not stable across calls", f.name)
		}
	}
	if len(seen) != len(named) {
		t.Fatalf("expected %d distinct fragments, recorded %d", len(named), len(seen))
	}
}

// TestFileCrypt_SealAlwaysProducesEnvelope closes the shortcut lane. The EMPTY
// payload case is the one that matters: an implementation that returned its
// input unchanged for a trivial payload would satisfy every other test here.
func TestFileCrypt_SealAlwaysProducesEnvelope(t *testing.T) {
	const path = "/tmp/filecrypt-envelope/record.bin"
	cases := []struct {
		name  string
		input []byte
	}{
		{"empty", []byte{}},
		{"large", bytes.Repeat([]byte("payload-"), 200_000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := Seal(path, tc.input)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if !bytes.HasPrefix(blob, magicKCE1) {
				t.Fatalf("sealed blob does not begin with the envelope magic: %x", blob[:min(len(blob), 8)])
			}
			want := len(tc.input) + headerLen + gcmTagLen
			if len(blob) != want {
				t.Fatalf("sealed length = %d, want %d (input %d + header %d + tag %d)",
					len(blob), want, len(tc.input), headerLen, gcmTagLen)
			}
		})
	}
}

// TestFileCrypt_RefusesEmptyMasterKey drives the per-file derivation with an
// INJECTED master key rather than through MasterKey(), because the real master
// key can never be empty — deriving through it would leave the guard untestable,
// which is the same defect as not having the guard at all.
//
// WHY AN APPARENTLY REDUNDANT LENGTH CHECK EXISTS: HKDF does not error on an
// empty secret. It returns 32 deterministic bytes derived from nothing but the
// path and the info string, so a missing master key would silently produce a key
// anyone could recompute rather than failing.
func TestFileCrypt_RefusesEmptyMasterKey(t *testing.T) {
	const path = "/tmp/filecrypt-guard/record.bin"
	cases := []struct {
		name   string
		master []byte
	}{
		{"nil", nil},
		{"thirty-one bytes", make([]byte, 31)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := deriveFileKey(tc.master, path)
			if !errors.Is(err, ErrNoMasterKey) {
				t.Fatalf("error = %v, want ErrNoMasterKey", err)
			}
			if key != nil {
				t.Fatalf("key bytes returned alongside the refusal: %x", key)
			}
		})
	}

	// Known-positive control, same run: a 32-byte master DOES derive, so the two
	// refusals above are the length guard and not a derivation that never works.
	key, err := deriveFileKey(make([]byte, 32), path)
	if err != nil {
		t.Fatalf("control: a 32-byte master should derive: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("control: derived key length = %d, want 32", len(key))
	}

	// The refusal reports the length it was handed and never the key material.
	if _, err := deriveFileKey(make([]byte, 31), path); err == nil || !strings.Contains(err.Error(), "31") {
		t.Fatalf("refusal should name the length it was given, got %v", err)
	}
}

// TestFileCrypt_NonceIsFreshPerSeal is the gate on nonce reuse, which under a
// single key is a total loss of GCM's guarantees rather than a style question.
//
// IT IS THE ONLY TEST IN THIS FILE THAT CATCHES IT. An implementation using one
// fixed package-level nonce passes the round trip, the path binding, the machine
// binding, the no-plaintext assertion, the envelope shape and the empty-master
// guard — all six — and fails only here.
func TestFileCrypt_NonceIsFreshPerSeal(t *testing.T) {
	const path = "/tmp/filecrypt-nonce/record.bin"
	payload := []byte("identical plaintext, sealed twice at one path")

	first, err := Seal(path, payload)
	if err != nil {
		t.Fatalf("first Seal: %v", err)
	}
	second, err := Seal(path, payload)
	if err != nil {
		t.Fatalf("second Seal: %v", err)
	}

	if bytes.Equal(first, second) {
		t.Fatal("sealing the same plaintext twice at the same path produced identical blobs")
	}

	// The nonce occupies bytes [5:17] — magic(4) + key version(1), then 12 bytes.
	firstNonce := first[5:17]
	secondNonce := second[5:17]
	if bytes.Equal(firstNonce, secondNonce) {
		t.Fatalf("nonce reused across seals: %x", firstNonce)
	}

	// KNOWN-POSITIVE CONTROL, same run: both blobs open to the SAME plaintext.
	// Without it, a corrupted seal would also produce two differing blobs and
	// this test would pass for the wrong reason.
	firstPlain, err := Open(path, first)
	if err != nil {
		t.Fatalf("control: Open first blob: %v", err)
	}
	secondPlain, err := Open(path, second)
	if err != nil {
		t.Fatalf("control: Open second blob: %v", err)
	}
	if !bytes.Equal(firstPlain, secondPlain) || !bytes.Equal(firstPlain, payload) {
		t.Fatalf("control: the two blobs did not open to the same original plaintext")
	}
}
