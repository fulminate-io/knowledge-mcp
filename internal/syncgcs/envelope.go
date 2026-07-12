// SPDX-License-Identifier: Apache-2.0

package syncgcs

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// SealEnvelope produces a PUSH object per the pinned contract (see
// envelope_spec.go): it generates a fresh 32-byte DEK, AES-256-GCM-seals the
// plaintext (12-byte random nonce, AAD = BuildAAD(EnvelopeDirectionPush,
// objectPath)), RSA-OAEP-SHA256-wraps the DEK with the agent's public key
// (parsed from PEM), and frames the result as
//
//	[u32 BE wrapped-DEK len][wrapped-DEK][12B nonce][AES-256-GCM ct+tag].
//
// objectPath is the agent-minted GCS object path from the presign response; the
// agent recomputes the SAME AAD on open from body.ObjectPath, so a tampered or
// mismatched path fails the GCM auth. The DEK is local to this call and discarded
// on return; only the agent's KMS private key can unwrap it. agentPubKeyPEM is
// the PKIX RSA public-key PEM returned in the presign response.
func SealEnvelope(plaintext []byte, agentPubKeyPEM, objectPath string) ([]byte, error) {
	pub, err := parseRSAPublicKeyPEM(agentPubKeyPEM)
	if err != nil {
		return nil, err
	}

	dek := make([]byte, EnvelopeDEKSize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("syncgcs: generate DEK: %w", err)
	}

	nonce := make([]byte, EnvelopeNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("syncgcs: generate nonce: %w", err)
	}

	ciphertext, err := sealGCM(dek, nonce, plaintext, BuildAAD(EnvelopeDirectionPush, objectPath))
	if err != nil {
		return nil, err
	}

	wrappedDEK, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, dek, nil)
	if err != nil {
		return nil, fmt.Errorf("syncgcs: RSA-OAEP wrap DEK: %w", err)
	}

	out := make([]byte, 0, EnvelopeWrappedDEKLenSize+len(wrappedDEK)+len(nonce)+len(ciphertext))
	lenPrefix := make([]byte, EnvelopeWrappedDEKLenSize)
	binary.BigEndian.PutUint32(lenPrefix, uint32(len(wrappedDEK)))
	out = append(out, lenPrefix...)
	out = append(out, wrappedDEK...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// OpenEnvelope reverses a PULL object ([12B nonce][AES-256-GCM ct+tag]) with the
// supplied DEK (returned plaintext in the pull JSON response) and AAD =
// BuildAAD(EnvelopeDirectionPull, objectPath), returning the recovered plaintext
// graph bytes. objectPath is the GCS object path the agent returned in the pull
// response; it MUST match what the agent sealed with or the GCM auth fails.
func OpenEnvelope(blob []byte, dek []byte, objectPath string) ([]byte, error) {
	if len(blob) < EnvelopeNonceSize {
		return nil, errors.New("syncgcs: pull object too short for nonce")
	}
	nonce := blob[:EnvelopeNonceSize]
	ciphertext := blob[EnvelopeNonceSize:]
	return openGCM(dek, nonce, ciphertext, BuildAAD(EnvelopeDirectionPull, objectPath))
}

// maxWrappedDEKLen bounds the u32 wrapped-DEK length prefix on a PUSH object so a
// corrupt/hostile header cannot drive a huge allocation before the GCM open. It
// mirrors the agent's readWrappedDEK cap (segment_fetch.go maxWrappedDEKLen) — an
// RSA-3072 wrapped DEK is 384 bytes; 8192 leaves ample headroom for 4096-bit keys.
const maxWrappedDEKLen = 8192

// OpenPushObject reverses a client-sealed PUSH object
// ([u32 BE wrappedDEKLen][wrapped-DEK][12B nonce][AES-256-GCM ct+tag], see
// envelope_spec.go) using a DEK the AGENT already unwrapped and returned (raw, in
// the fetch JSON response). It SKIPS the wrapped-DEK header (the client does not
// hold the KMS private key and never unwraps it itself) and GCM-opens the trailing
// [nonce][ct] with AAD = BuildAAD(EnvelopeDirectionPush, objectPath), returning the
// recovered plaintext segment bytes. objectPath is the GCS object path the agent
// returned in the fetch response; it MUST byte-match what the client sealed with or
// the GCM auth fails. OpenEnvelope (the PULL-shape decoder) CANNOT open a push
// object: it has no wrapped-DEK header and uses the pull-direction AAD.
func OpenPushObject(blob, dek []byte, objectPath string) ([]byte, error) {
	if len(blob) < EnvelopeWrappedDEKLenSize {
		return nil, errors.New("syncgcs: push object too short for length prefix")
	}
	wrappedLen := binary.BigEndian.Uint32(blob[:EnvelopeWrappedDEKLenSize])
	if wrappedLen == 0 || wrappedLen > maxWrappedDEKLen {
		return nil, fmt.Errorf("syncgcs: wrapped-DEK length %d out of range (1..%d)", wrappedLen, maxWrappedDEKLen)
	}
	end := EnvelopeWrappedDEKLenSize + int(wrappedLen)
	if end > len(blob) {
		return nil, fmt.Errorf("syncgcs: wrapped-DEK length %d overruns push object of %d bytes", wrappedLen, len(blob))
	}
	rest := blob[end:]
	if len(rest) < EnvelopeNonceSize {
		return nil, errors.New("syncgcs: push object too short for nonce")
	}
	nonce := rest[:EnvelopeNonceSize]
	ciphertext := rest[EnvelopeNonceSize:]
	return openGCM(dek, nonce, ciphertext, BuildAAD(EnvelopeDirectionPush, objectPath))
}

// sealGCM AES-256-GCM-encrypts plaintext under key with nonce and the supplied
// AAD, returning ciphertext+tag (nonce framed separately by the caller).
func sealGCM(key, nonce, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("syncgcs: nonce must be %d bytes", gcm.NonceSize())
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

// openGCM AES-256-GCM-decrypts ciphertext under key with nonce and the supplied
// AAD. A tag/AAD mismatch returns an authentication error.
func openGCM(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("syncgcs: nonce must be %d bytes", gcm.NonceSize())
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("syncgcs: GCM open (auth): %w", err)
	}
	return plain, nil
}

// newGCM builds an AES-256-GCM AEAD over the given 32-byte key.
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != EnvelopeDEKSize {
		return nil, fmt.Errorf("syncgcs: key must be %d bytes (AES-256)", EnvelopeDEKSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// parseRSAPublicKeyPEM parses a PKIX RSA public-key PEM (the shape KMS
// GetPublicKey returns). It mirrors the stdlib x509/pem usage in the agent's
// license verification (re-authored, not imported — cross-repo).
func parseRSAPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("syncgcs: agent public key is not valid PEM")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("syncgcs: parse PKIX public key: %w", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("syncgcs: agent public key is %T, want RSA", pubAny)
	}
	return pub, nil
}
