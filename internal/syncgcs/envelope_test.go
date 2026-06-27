// SPDX-License-Identifier: Apache-2.0

package syncgcs

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"testing"
)

// testObjectPath is a representative agent-minted GCS object path used as the AAD
// binding component across the envelope tests.
const testObjectPath = "sync/acct-1/knowledge/default/uuid.kgenv"

// TestSealEnvelopeCrossImplementationConformance is the cross-implementation
// conformance vector: it seals with a generated RSA public key and confirms the
// wrapped DEK RSA-OAEP-SHA256-decrypts with the private key and the GCM
// ciphertext opens with that DEK + the push-direction AAD for the SAME object
// path — i.e. exactly what the AGENT's kmscrypto.UnwrapDEK + crypto.OpenGCM(...,
// buildEnvelopeAAD(push, body.ObjectPath)) do on the push-confirm path. This
// proves the client seal and the agent open are inverse halves of the contract.
func TestSealEnvelopeCrossImplementationConformance(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubPEM := mustPublicKeyPEM(t, &priv.PublicKey)

	plaintext := []byte("KGV4 a representative serialized knowledge graph image, of nontrivial length")
	blob, err := SealEnvelope(plaintext, pubPEM, testObjectPath)
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}

	// Parse the push framing exactly as the agent's parsePushEnvelope does.
	if len(blob) < EnvelopeWrappedDEKLenSize {
		t.Fatal("blob too short for length prefix")
	}
	wrappedLen := binary.BigEndian.Uint32(blob[:EnvelopeWrappedDEKLenSize])
	off := EnvelopeWrappedDEKLenSize
	end := off + int(wrappedLen)
	if end > len(blob) {
		t.Fatalf("wrapped-DEK length %d overruns blob %d", wrappedLen, len(blob))
	}
	wrappedDEK := blob[off:end]
	rest := blob[end:]
	if len(rest) < EnvelopeNonceSize {
		t.Fatal("blob too short for nonce")
	}
	nonce := rest[:EnvelopeNonceSize]
	ciphertext := rest[EnvelopeNonceSize:]

	// Agent side: RSA-OAEP-SHA256 unwrap (KMS AsymmetricDecrypt equivalent).
	dek, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, wrappedDEK, nil)
	if err != nil {
		t.Fatalf("DecryptOAEP (agent unwrap): %v", err)
	}
	if len(dek) != EnvelopeDEKSize {
		t.Fatalf("DEK len = %d, want %d", len(dek), EnvelopeDEKSize)
	}

	// Agent side: GCM open with the DEK + nonce + push-direction AAD for the path.
	pushAAD := BuildAAD(EnvelopeDirectionPush, testObjectPath)
	got := mustGCMOpen(t, dek, nonce, ciphertext, pushAAD)
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("recovered %q, want %q", got, plaintext)
	}

	// Wrong object path in the AAD must fail (binds the path).
	if _, err := gcmOpen(dek, nonce, ciphertext, BuildAAD(EnvelopeDirectionPush, "sync/acct-2/x/y/z.kgenv")); err == nil {
		t.Fatal("GCM open with a DIFFERENT object path should fail")
	}
	// Wrong direction (pull) must fail (binds the direction).
	if _, err := gcmOpen(dek, nonce, ciphertext, BuildAAD(EnvelopeDirectionPull, testObjectPath)); err == nil {
		t.Fatal("GCM open with the PULL direction should fail")
	}
}

// TestOpenEnvelopePullRoundTrip proves OpenEnvelope reverses the pull shape
// [nonce][ciphertext] produced by the agent's SealGCM + pull-direction AAD for
// the SAME object path.
func TestOpenEnvelopePullRoundTrip(t *testing.T) {
	dek := make([]byte, EnvelopeDEKSize)
	if _, err := rand.Read(dek); err != nil {
		t.Fatalf("rand dek: %v", err)
	}
	nonce := make([]byte, EnvelopeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	plaintext := []byte("KGV4 pull graph image")

	// Agent seal: pull-direction AAD for the object path.
	block, _ := aes.NewCipher(dek)
	gcm, _ := cipher.NewGCM(block)
	ct := gcm.Seal(nil, nonce, plaintext, BuildAAD(EnvelopeDirectionPull, testObjectPath))

	// Pull object = [nonce][ct].
	object := append(append([]byte{}, nonce...), ct...)

	got, err := OpenEnvelope(object, dek, testObjectPath)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("OpenEnvelope recovered %q, want %q", got, plaintext)
	}

	// OpenEnvelope with the WRONG object path must fail (AAD binds the path).
	if _, err := OpenEnvelope(object, dek, "sync/acct-2/other.kgenv"); err == nil {
		t.Fatal("OpenEnvelope with a different object path must fail")
	}
}

func TestOpenEnvelopeRejectsShortBlob(t *testing.T) {
	dek := make([]byte, EnvelopeDEKSize)
	if _, err := OpenEnvelope([]byte{0x01, 0x02}, dek, testObjectPath); err == nil {
		t.Fatal("OpenEnvelope on too-short blob should fail")
	}
}

func TestSealEnvelopeRejectsBadPEM(t *testing.T) {
	if _, err := SealEnvelope([]byte("x"), "not a pem", testObjectPath); err == nil {
		t.Fatal("SealEnvelope with bad PEM should fail")
	}
}

// TestBuildAADMatchesAcrossDirectionsAndPaths pins the AAD byte layout:
// version || 0x00 || direction || 0x00 || objectPath, and that distinct
// directions/paths produce distinct AADs.
func TestBuildAADDistinct(t *testing.T) {
	push := BuildAAD(EnvelopeDirectionPush, testObjectPath)
	pull := BuildAAD(EnvelopeDirectionPull, testObjectPath)
	otherPath := BuildAAD(EnvelopeDirectionPush, "sync/acct-2/x.kgenv")
	if bytes.Equal(push, pull) {
		t.Fatal("push and pull AADs must differ")
	}
	if bytes.Equal(push, otherPath) {
		t.Fatal("AADs for different object paths must differ")
	}
	want := append(append(append(append([]byte(EnvelopeVersion), 0x00), []byte(EnvelopeDirectionPush)...), 0x00), []byte(testObjectPath)...)
	if !bytes.Equal(push, want) {
		t.Fatalf("AAD layout = %q, want %q", push, want)
	}
}

// TestSealEnvelopeLargeGraphParity proves a representative LARGE serialized graph
// survives the push seal + (agent-side) unwrap/open to IDENTICAL bytes — the
// per-component stand-in for the >100MB E2E. The envelope path is size-agnostic;
// an 8 MiB payload exercises the framing + GCM at scale without a slow alloc.
func TestSealEnvelopeLargeGraphParity(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	plaintext := make([]byte, 8<<20)
	copy(plaintext, []byte("KGV4"))
	if _, err := rand.Read(plaintext[4:]); err != nil {
		t.Fatalf("rand: %v", err)
	}

	blob, err := SealEnvelope(plaintext, mustPublicKeyPEM(t, &priv.PublicKey), testObjectPath)
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}

	wrappedLen := binary.BigEndian.Uint32(blob[:EnvelopeWrappedDEKLenSize])
	off := EnvelopeWrappedDEKLenSize + int(wrappedLen)
	nonce := blob[off : off+EnvelopeNonceSize]
	ct := blob[off+EnvelopeNonceSize:]
	dek, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, blob[EnvelopeWrappedDEKLenSize:EnvelopeWrappedDEKLenSize+int(wrappedLen)], nil)
	if err != nil {
		t.Fatalf("DecryptOAEP: %v", err)
	}
	got := mustGCMOpen(t, dek, nonce, ct, BuildAAD(EnvelopeDirectionPush, testObjectPath))
	if !bytes.Equal(got, plaintext) {
		t.Fatal("large-graph envelope did not round-trip to identical bytes")
	}
}

// TestOpenEnvelopeWrongDEKFails proves a pull GCS object is opaque without the
// returned DEK: OpenEnvelope under a wrong DEK fails — no plaintext recovered.
func TestOpenEnvelopeWrongDEKFails(t *testing.T) {
	dek := make([]byte, EnvelopeDEKSize)
	if _, err := rand.Read(dek); err != nil {
		t.Fatalf("rand: %v", err)
	}
	nonce := make([]byte, EnvelopeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand: %v", err)
	}
	block, _ := aes.NewCipher(dek)
	gcm, _ := cipher.NewGCM(block)
	object := append(append([]byte{}, nonce...), gcm.Seal(nil, nonce, []byte("KGV4 secret"), BuildAAD(EnvelopeDirectionPull, testObjectPath))...)

	wrongDEK := make([]byte, EnvelopeDEKSize)
	if _, err := rand.Read(wrongDEK); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := OpenEnvelope(object, wrongDEK, testObjectPath); err == nil {
		t.Fatal("OpenEnvelope with a wrong DEK must fail — pull object must be opaque without the returned DEK")
	}
}

func mustPublicKeyPEM(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// gcmOpen / mustGCMOpen are test helpers that AES-256-GCM-open under the given AAD.
func gcmOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func mustGCMOpen(t *testing.T, key, nonce, ciphertext, aad []byte) []byte {
	t.Helper()
	got, err := gcmOpen(key, nonce, ciphertext, aad)
	if err != nil {
		t.Fatalf("GCM open: %v", err)
	}
	return got
}
