// Package syncgcs implements the knowledge client's half of the
// presigned-direct-to-GCS sync transport: the asymmetric-envelope crypto
// (AES-256-GCM seal/open + RSA-OAEP DEK wrap) and the no-auth net/http GCS
// PUT/GET. It is pure Go stdlib (no cloud SDK symbols) so it does not perturb
// the OSS no-cloud-symbol gate.
//
// # INFRA PRE-REQUISITE — sync GCS bucket object-lifecycle rule
//
// The sync GCS bucket MUST be provisioned out-of-band, BEFORE the agent runs,
// with an object-lifecycle rule that deletes objects under the sync/ prefix after
// a short age (e.g. AgeInDays 1). This is the orphan-object / stale-ciphertext
// backstop for the objects the agent does NOT delete inline: a PULL object the
// client GETs out-of-band (the agent cannot observe the GET), and a PUSH object
// whose confirm hit a transient (5xx) upstream and was left for retry. The agent
// deletes its OWN objects inline on terminal confirm outcomes (2xx/4xx); the
// bucket lifecycle rule is the belt-and-braces reaper. Bucket-level configuration
// (lifecycle, versioning, IAM) is owned by INFRA, not the agent — the agent never
// touches bucket config. This requirement is a HARD operational pre-req: without
// the lifecycle rule, abandoned ciphertext accumulates in the bucket.
//
// # Sync envelope contract (v1) — THE PINNED CRYPTO SPEC
//
// This doc block is the canonical, security-reviewed contract that the client
// (this package) PRODUCES and the agent (cmd/agent confirm/pull handlers)
// CONSUMES. The literal constants below are mirrored verbatim as consts on the
// agent decrypt side; any change here is a wire-format change requiring explicit
// re-approval and a coordinated update on both sides.
//
// ## Two object layouts — push and pull are DISTINCT
//
// PUSH object (client seals, agent opens):
//
//		┌──────────────────────┬───────────────────┬───────────┬──────────────────────┐
//		│ u32 BE wrappedDEKLen │ wrapped-DEK bytes │ 12B nonce │ AES-256-GCM ct + tag │
//		└──────────────────────┴───────────────────┴───────────┴──────────────────────┘
//
//	  - The wrapped-DEK is the per-request DEK encrypted to the agent's PUBLIC key
//	    with RSA-OAEP-SHA256. For an RSA-3072 key its length is 384 bytes, but it
//	    is LENGTH-PREFIXED (u32 big-endian), NOT fixed-offset, so an RSA key-size
//	    change (e.g. 3072 -> 4096) does not silently corrupt parsing.
//	  - Only an agent replica holding the KMS private key can unwrap the DEK, so
//	    only the agent can decrypt a push object.
//
// PULL object (agent seals, client opens):
//
//		┌───────────┬──────────────────────┐
//		│ 12B nonce │ AES-256-GCM ct + tag │
//		└───────────┴──────────────────────┘
//
//	  - There is NO wrapped-DEK in a pull object. The agent generated the DEK and
//	    returns it (plaintext) in the JSON pull response to the AUTHENTICATED
//	    puller only. The GCS object at rest is therefore ciphertext, undecryptable
//	    by a third party who lacks the returned DEK.
//
// ## Crypto parameters (identical for both layouts)
//
//   - DEK: EnvelopeDEKSize (32) random bytes from crypto/rand, per-request,
//     never reused. On push the client discards it after the PUT; on pull the
//     agent returns it to the puller.
//   - AEAD: AES-256-GCM. Nonce = EnvelopeNonceSize (12) random bytes from
//     crypto/rand. AAD = BuildAAD(direction, objectPath) =
//     EnvelopeVersion || 0x00 || direction || 0x00 || objectPath — a context
//     label binding the ciphertext to (this envelope version, its transfer
//     DIRECTION, and its exact GCS object path). direction is
//     EnvelopeDirectionPush for push objects and EnvelopeDirectionPull for pull
//     objects; objectPath already embeds the account id + graphType + name, so
//     binding it ties a ciphertext to one account's one object — a ciphertext
//     cannot be replayed under a different path, a different direction, or a
//     different envelope version (any mismatch fails the GCM auth on open). The
//     AAD is NOT machine-id-derived and NOT password-derived. The seal and open
//     sides MUST compute byte-identical AAD per direction (push: the client
//     seals and the agent opens with body.ObjectPath; pull: the agent seals and
//     the client opens with the object_path returned in the pull response).
//   - DEK wrap (push only): rsa.EncryptOAEP(sha256.New(), rand, agentPub, dek,
//     nil) — OAEP label nil, hash SHA-256. This MUST match the agent KMS key's
//     RSA_DECRYPT_OAEP_3072_SHA256 algorithm (SHA-256 on both sides).
//
// ## Integrity
//
// GCM's auth tag authenticates the ciphertext under the DEK + AAD. Additional
// integrity is provided downstream by the agent's KGV4 magic-header check and
// plaintext size cap after decrypt, and by the knowledge-server's existing
// maxDeserializeLen decompression-bomb backstop. The envelope therefore
// deliberately OMITS the server bootstrap envelope's extra sha256 plaintext
// checksum — it would be redundant defense-in-depth here.
//
// ## NOT reused from the server bootstrap envelope
//
// cmd/knowledge-server/internal/bootstrap/envelope.go (sealEnvelope/openEnvelope)
// is the byte-layout SHAPE template only (GCM Seal/Open with a header-as-AAD).
// It is NOT imported (separate build the client cannot reach) and its KEY
// SCHEDULE is fundamentally different — it derives its key from a password via
// an HKDF/pepper schedule, whereas this envelope uses a fresh random per-request
// DEK. Borrow the shape; do not borrow the password/pepper key derivation.
package syncgcs

// EnvelopeVersion is the version tag for the sync envelope contract; it is the
// first AAD component (see BuildAAD). Bump only with a coordinated wire change.
const EnvelopeVersion = "fulminate-sync-envelope-v1"

// EnvelopeDirection labels the transfer direction in the AAD so a push ciphertext
// can never be opened as a pull ciphertext (or vice versa) even at the same path.
const (
	EnvelopeDirectionPush = "push"
	EnvelopeDirectionPull = "pull"
)

const (
	// EnvelopeDEKSize is the per-request Data Encryption Key length in bytes
	// (AES-256 -> 32 bytes).
	EnvelopeDEKSize = 32

	// EnvelopeNonceSize is the AES-256-GCM nonce length in bytes (the standard
	// 96-bit GCM nonce).
	EnvelopeNonceSize = 12

	// EnvelopeWrappedDEKLenSize is the size of the big-endian u32 length prefix
	// that precedes the wrapped-DEK in a PUSH object.
	EnvelopeWrappedDEKLenSize = 4
)

// BuildAAD computes the AES-256-GCM Additional Authenticated Data for an
// envelope: EnvelopeVersion || 0x00 || direction || 0x00 || objectPath. The
// 0x00 separators are unambiguous because none of the three components contains
// a NUL byte (version + direction are fixed ASCII; objectPath is a GCS object
// name). Binding the version pins the contract; binding the direction prevents a
// push<->pull confusion; binding objectPath (which embeds account + graphType +
// name) ties the ciphertext to exactly one account's one object. The seal and
// open sides MUST pass the SAME direction + objectPath or the GCM auth fails.
// This is the AGENT-MIRRORED contract — keep byte-identical to the agent's
// buildEnvelopeAAD.
func BuildAAD(direction, objectPath string) []byte {
	aad := make([]byte, 0, len(EnvelopeVersion)+1+len(direction)+1+len(objectPath))
	aad = append(aad, EnvelopeVersion...)
	aad = append(aad, 0x00)
	aad = append(aad, direction...)
	aad = append(aad, 0x00)
	aad = append(aad, objectPath...)
	return aad
}
