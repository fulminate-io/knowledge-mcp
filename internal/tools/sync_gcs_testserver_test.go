// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// sealGCMForTest / openGCMForTest mirror the AES-256-GCM seal/open the agent and
// client perform, using the supplied (direction+path-bound) AAD, so the test
// backend produces and consumes envelopes byte-identically to the production
// crypto.
func sealGCMForTest(key, nonce, plaintext, aad []byte) []byte {
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	return gcm.Seal(nil, nonce, plaintext, aad)
}

func openGCMForTest(key, nonce, ciphertext, aad []byte) ([]byte, error) {
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

// fakeSyncBackend stands up a single httptest server that plays BOTH roles in the
// presigned-GCS sync flow: the agent control plane (/v1/sync/presign, /confirm,
// /pull) AND GCS object storage (the presigned PUT/GET URLs it hands back point
// at its own /gcs/<id> routes). It holds a real RSA-3072 keypair so push
// envelopes seal to a key it can unwrap and pull objects are produced exactly as
// the agent would — making the tests true end-to-end crypto round-trips.
type fakeSyncBackend struct {
	srv  *httptest.Server
	priv *rsa.PrivateKey

	mu sync.Mutex
	// objects holds GCS object bodies keyed by object id.
	objects map[string][]byte
	// pullPlaintext is the bytes /v1/sync/pull will encrypt + serve (the
	// agent-exported graph); set per test.
	pullPlaintext []byte

	// pullUnchanged makes /v1/sync/pull answer the unchanged short-circuit: no object
	// is produced or stored, and the response carries no download_url or DEK.
	pullUnchanged bool
	// pullWatermark is the token /v1/sync/pull returns on either path; set per test.
	pullWatermark string
	// lastPullWatermark records the watermark the CLIENT sent on the latest pull —
	// the only way to prove the stored token actually reached the wire.
	lastPullWatermark string

	presignCalls int
	confirmCalls int
	pullCalls    int
	// gcsGets counts GET hits on the object routes, so a test can assert the
	// unchanged path performed no download at all.
	gcsGets int
	// confirmedPlaintext is the decrypted bytes the LATEST confirm recovered
	// (proves the push ciphertext round-trips through the agent's unwrap+open).
	confirmedPlaintext []byte
}

func newFakeSyncBackend(t *testing.T) *fakeSyncBackend {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	b := &fakeSyncBackend{priv: priv, objects: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sync/presign", b.handlePresign)
	mux.HandleFunc("/v1/sync/confirm", b.handleConfirm)
	mux.HandleFunc("/v1/sync/pull", b.handlePull)
	mux.HandleFunc("/gcs/", b.handleGCS)
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeSyncBackend) handlePresign(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.presignCalls++
	b.mu.Unlock()
	objID := "push-obj"
	resp := syncPresignResponse{
		UploadURL:      b.srv.URL + "/gcs/" + objID,
		ObjectPath:     "sync/acct/" + objID,
		AgentPublicKey: pemFromKey(&b.priv.PublicKey),
		Expiry:         "2099-01-01T00:00:00Z",
	}
	writeTestJSON(w, resp)
}

func (b *fakeSyncBackend) handleConfirm(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.confirmCalls++
	b.mu.Unlock()
	var req syncConfirmRequest
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// Mirror the agent confirm: download the object, parse the push envelope,
	// unwrap the DEK (RSA-OAEP-SHA256), GCM-open with the push-direction AAD bound
	// to req.ObjectPath (exactly what the agent confirm handler does).
	objID := lastSegment(req.ObjectPath)
	b.mu.Lock()
	blob := b.objects[objID]
	b.mu.Unlock()
	plaintext, err := openPushEnvelope(b.priv, blob, syncgcs.BuildAAD(syncgcs.EnvelopeDirectionPush, req.ObjectPath))
	if err != nil {
		http.Error(w, "decrypt: "+err.Error(), http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	b.confirmedPlaintext = plaintext
	b.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (b *fakeSyncBackend) handlePull(w http.ResponseWriter, r *http.Request) {
	var req syncPullRequest
	reqBody, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(reqBody, &req)

	b.mu.Lock()
	b.pullCalls++
	b.lastPullWatermark = req.Watermark
	plaintext := b.pullPlaintext
	unchanged := b.pullUnchanged
	watermark := b.pullWatermark
	b.mu.Unlock()

	// The unchanged short-circuit: no object is minted, sealed or stored, so the
	// response carries no download_url and no DEK. A client that tried to download
	// anyway would fail loudly rather than silently fetching stale bytes.
	if unchanged {
		writeTestJSON(w, syncPullResponse{Unchanged: true, Watermark: watermark})
		return
	}

	// Produce a pull object exactly as the agent does: mint the path FIRST, then
	// per-request DEK + nonce, GCM seal with the pull-direction AAD bound to the
	// object path, frame as [nonce][ciphertext].
	objID := "pull-obj"
	objectPath := "sync/acct/" + objID
	dek := make([]byte, syncgcs.EnvelopeDEKSize)
	_, _ = rand.Read(dek)
	nonce := make([]byte, syncgcs.EnvelopeNonceSize)
	_, _ = rand.Read(nonce)
	ct := sealGCMForTest(dek, nonce, plaintext, syncgcs.BuildAAD(syncgcs.EnvelopeDirectionPull, objectPath))
	object := append(append([]byte{}, nonce...), ct...)

	b.mu.Lock()
	b.objects[objID] = object
	b.mu.Unlock()

	resp := syncPullResponse{
		DownloadURL: b.srv.URL + "/gcs/" + objID,
		DEK:         base64.StdEncoding.EncodeToString(dek),
		ObjectPath:  objectPath,
		Expiry:      "2099-01-01T00:00:00Z",
		Watermark:   watermark,
	}
	writeTestJSON(w, resp)
}

// writeTestJSON marshals v to w, panicking on a marshal error (test-only).
func writeTestJSON(w io.Writer, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	_, _ = w.Write(raw)
}

func (b *fakeSyncBackend) handleGCS(w http.ResponseWriter, r *http.Request) {
	objID := lastSegment(r.URL.Path)
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.objects[objID] = body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		b.mu.Lock()
		b.gcsGets++
		body, ok := b.objects[objID]
		b.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func lastSegment(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func pemFromKey(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// openPushEnvelope mirrors the agent confirm path: parse
// [u32 wrappedDEKLen][wrapped-DEK][nonce][ct], RSA-OAEP-SHA256 unwrap the DEK,
// AES-256-GCM open with the supplied (push-direction, path-bound) AAD.
func openPushEnvelope(priv *rsa.PrivateKey, blob, aad []byte) ([]byte, error) {
	if len(blob) < syncgcs.EnvelopeWrappedDEKLenSize {
		return nil, errShort
	}
	wrappedLen := int(uint32(blob[0])<<24 | uint32(blob[1])<<16 | uint32(blob[2])<<8 | uint32(blob[3]))
	off := syncgcs.EnvelopeWrappedDEKLenSize
	end := off + wrappedLen
	if end > len(blob) {
		return nil, errShort
	}
	wrapped := blob[off:end]
	rest := blob[end:]
	if len(rest) < syncgcs.EnvelopeNonceSize {
		return nil, errShort
	}
	nonce := rest[:syncgcs.EnvelopeNonceSize]
	ct := rest[syncgcs.EnvelopeNonceSize:]

	dek, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, wrapped, nil)
	if err != nil {
		return nil, err
	}
	return openGCMForTest(dek, nonce, ct, aad)
}

var errShort = errorString("envelope too short")

type errorString string

func (e errorString) Error() string { return string(e) }
