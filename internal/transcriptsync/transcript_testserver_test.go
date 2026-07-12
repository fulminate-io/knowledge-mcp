// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// fakeTranscriptBackend plays BOTH roles of the transcript-mode sync flow: the
// agent control plane (SyncControlJSON for presign/confirm/consent — called
// in-process, satisfying ControlTransport directly) AND GCS object storage (an
// httptest server the presigned PUT URLs point at). It holds a real RSA-3072
// keypair so push envelopes seal to a key it can unwrap, making the tests true
// end-to-end crypto round-trips. Mirrors tools' fakeSyncBackend.
type fakeTranscriptBackend struct {
	srv  *httptest.Server
	priv *rsa.PrivateKey

	mu      sync.Mutex
	objects map[string][]byte

	presignCalls int
	confirmCalls int
	consentCalls int

	// confirms records every per-element confirm in arrival order (the run tests
	// inspect which sessions confirmed). Elements are confirmBatchChunk — the
	// surviving batch wire type — since the batch path is the only transcript path.
	confirms []confirmBatchChunk

	// Failure injection for the ship tests.
	presignErr bool
	confirmErr bool
	// failConfirmSession, when set, makes confirm fail ONLY for that session — so a
	// run test can fail file B while file A succeeds (per-file isolation). On the
	// batch path it yields a per-element OK:false for that session (HTTP 200).
	failConfirmSession string
	// Batch length-mismatch injection (T3-1): when set, the batch handler returns a
	// results array of the WRONG length so the client's length guard fires before any
	// positional pairing.
	presignWrongLen bool
	confirmWrongLen bool
	// Intermittent batch transport-error injection: the first N presign-batch /
	// confirm-batch calls fail (then succeed) — for the no-deadlock test where SOME
	// batches fail while unaffected files still advance.
	presignFailFirstN int
	confirmFailFirstN int

	// Consent control for the gate tests: consentErr forces a fetch failure
	// (skip-and-retry); otherwise the response carries consentEnabledFlag.
	consentErr         bool
	consentEnabledFlag bool
}

func newFakeTranscriptBackend(t *testing.T) *fakeTranscriptBackend {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	b := &fakeTranscriptBackend{priv: priv, objects: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/gcs/", b.handleGCS)
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

// SyncControlJSON makes the backend a ControlTransport: presign hands back a
// signed PUT URL + the agent pubkey; confirm downloads, unwraps, and GCM-opens
// the object exactly as the agent would; transcript-consent returns the flag.
func (b *fakeTranscriptBackend) SyncControlJSON(_ context.Context, path string, body []byte) ([]byte, error) {
	switch path {
	case "presign-batch":
		return b.handlePresignBatch(body)
	case "confirm-batch":
		return b.handleConfirmBatch(body)
	case consentControlPath:
		return b.handleConsent(body)
	default:
		return nil, fmt.Errorf("fake backend: unexpected control path %q", path)
	}
}

// presignElement builds the presign reply for one session's object identity — a
// per-session sealed-staging path + signed GCS PUT URL, mirroring the agent's
// account-scoped staging mint, looped by the batch presign handler.
func (b *fakeTranscriptBackend) presignElement(source, session string) presignResponse {
	objectPath := fmt.Sprintf("transcripts-staging/acct/%s/%s.parquet", source, session)
	seg := base64.RawURLEncoding.EncodeToString([]byte(objectPath))
	return presignResponse{
		UploadURL:      b.srv.URL + "/gcs/" + seg,
		ObjectPath:     objectPath,
		AgentPublicKey: pemFromKey(&b.priv.PublicKey),
		Expiry:         "2099-01-01T00:00:00Z",
	}
}

// confirmRecordAndDecrypt records a per-element confirm in arrival order, applies
// failure injection (confirmErr or failConfirmSession), and on success unwraps +
// GCM-opens the uploaded object exactly as the agent confirm would (proving the push
// ciphertext round-trips). It returns "" on success or a per-element error code.
func (b *fakeTranscriptBackend) confirmRecordAndDecrypt(req confirmBatchChunk) string {
	b.mu.Lock()
	b.confirms = append(b.confirms, req)
	failed := b.confirmErr || (b.failConfirmSession != "" && req.Session == b.failConfirmSession)
	blob := b.objects[req.ObjectPath]
	b.mu.Unlock()
	if failed {
		return "confirm_rejected"
	}
	if _, err := openPushEnvelope(b.priv, blob,
		syncgcs.BuildAAD(syncgcs.EnvelopeDirectionPush, req.ObjectPath)); err != nil {
		return "decrypt_failed"
	}
	return ""
}

// handlePresignBatch decodes the batch envelope and returns one presignResponse per
// chunk (in request order), incrementing presignCalls ONCE per batch so the
// request-count assertions are meaningful. presignErr forces a whole-request
// transport error; presignWrongLen returns a wrong-length array (T3-1 guard test).
func (b *fakeTranscriptBackend) handlePresignBatch(body []byte) ([]byte, error) {
	b.mu.Lock()
	b.presignCalls++
	failed := b.presignErr
	if b.presignFailFirstN > 0 {
		b.presignFailFirstN--
		failed = true
	}
	wrongLen := b.presignWrongLen
	b.mu.Unlock()
	if failed {
		return nil, fmt.Errorf("fake backend: presign-batch rejected")
	}
	var req presignBatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	resp := presignBatchResponse{Chunks: make([]presignResponse, 0, len(req.Chunks))}
	for _, ch := range req.Chunks {
		resp.Chunks = append(resp.Chunks, b.presignElement(ch.Source, ch.Session))
	}
	if wrongLen && len(resp.Chunks) > 0 {
		resp.Chunks = resp.Chunks[:len(resp.Chunks)-1] // drop one → length mismatch.
	}
	return json.Marshal(resp)
}

// handleConfirmBatch decodes the batch envelope, confirms each element (recording it
// + applying per-element failure injection), and returns a request-order-parallel
// results array. confirmCalls increments ONCE per batch. confirmErr forces a
// whole-request transport error; confirmWrongLen returns a wrong-length results array.
func (b *fakeTranscriptBackend) handleConfirmBatch(body []byte) ([]byte, error) {
	b.mu.Lock()
	b.confirmCalls++
	failed := b.confirmErr
	if b.confirmFailFirstN > 0 {
		b.confirmFailFirstN--
		failed = true
	}
	wrongLen := b.confirmWrongLen
	b.mu.Unlock()
	if failed {
		return nil, fmt.Errorf("fake backend: confirm-batch rejected")
	}
	var req confirmBatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	resp := confirmBatchResponse{Results: make([]confirmElementResult, 0, len(req.Chunks))}
	for _, ch := range req.Chunks {
		if code := b.confirmRecordAndDecrypt(ch); code != "" {
			resp.Results = append(resp.Results, confirmElementResult{OK: false, Error: code})
		} else {
			resp.Results = append(resp.Results, confirmElementResult{OK: true})
		}
	}
	if wrongLen && len(resp.Results) > 0 {
		resp.Results = resp.Results[:len(resp.Results)-1] // drop one → length mismatch.
	}
	return json.Marshal(resp)
}

func (b *fakeTranscriptBackend) handleConsent(_ []byte) ([]byte, error) {
	b.mu.Lock()
	b.consentCalls++
	failed := b.consentErr
	enabled := b.consentEnabledFlag
	b.mu.Unlock()
	if failed {
		return nil, fmt.Errorf("fake backend: consent fetch failed")
	}
	return json.Marshal(consentResponse{TranscriptCollectionEnabled: enabled})
}

func (b *fakeTranscriptBackend) handleGCS(w http.ResponseWriter, r *http.Request) {
	// The URL segment is the base64url-encoded full object path; decode it back so
	// objects are keyed by the SAME path the confirm carries.
	seg := lastSegment(r.URL.Path)
	pathBytes, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		http.Error(w, "bad object segment", http.StatusBadRequest)
		return
	}
	key := string(pathBytes)
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.objects[key] = body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		b.mu.Lock()
		body, ok := b.objects[key]
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

// confirmsForSession returns the confirm bodies recorded for one session in
// arrival order (the run tests assert which sessions confirmed).
func (b *fakeTranscriptBackend) confirmsForSession(session string) []confirmBatchChunk {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []confirmBatchChunk
	for _, c := range b.confirms {
		if c.Session == session {
			out = append(out, c)
		}
	}
	return out
}

// putObjectCount reports how many GCS objects were stored (proves a PUT happened
// or did not).
func (b *fakeTranscriptBackend) putObjectCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// --- test-only crypto helpers (mirror the agent confirm path) ---

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

// openPushEnvelope mirrors the agent confirm: parse
// [u32 wrappedDEKLen][wrapped-DEK][nonce][ct], RSA-OAEP-SHA256 unwrap the DEK,
// AES-256-GCM open with the supplied (push-direction, path-bound) AAD.
func openPushEnvelope(priv *rsa.PrivateKey, blob, aad []byte) ([]byte, error) {
	if len(blob) < syncgcs.EnvelopeWrappedDEKLenSize {
		return nil, fmt.Errorf("envelope too short")
	}
	wrappedLen := int(uint32(blob[0])<<24 | uint32(blob[1])<<16 | uint32(blob[2])<<8 | uint32(blob[3]))
	off := syncgcs.EnvelopeWrappedDEKLenSize
	end := off + wrappedLen
	if end > len(blob) {
		return nil, fmt.Errorf("envelope too short")
	}
	wrapped := blob[off:end]
	rest := blob[end:]
	if len(rest) < syncgcs.EnvelopeNonceSize {
		return nil, fmt.Errorf("envelope too short")
	}
	nonce := rest[:syncgcs.EnvelopeNonceSize]
	ct := rest[syncgcs.EnvelopeNonceSize:]

	dek, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, wrapped, nil)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ct, aad)
}
