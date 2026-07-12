// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// fakeSegmentBackend plays BOTH roles of the GCS-agent segment flow: the agent
// control plane (SegmentControlJSON for presign-batch / fetch-batch / manifest
// publish+read — called in-process, satisfying segmentControlTransport directly)
// AND GCS object storage (an httptest server the presigned PUT/GET URLs point at).
// It holds a real RSA-3072 keypair so PUSH envelopes seal to a key it can unwrap on
// fetch, making the tests true end-to-end crypto round-trips. Mirrors
// transcriptsync's fakeTranscriptBackend.
type fakeSegmentBackend struct {
	srv  *httptest.Server
	priv *rsa.PrivateKey

	mu      sync.Mutex
	objects map[string][]byte // object_path -> stored (sealed) bytes
	// manifests keyed by "<gt>/<name>/<format>" -> published digests.
	manifests map[string][]manifestDigest

	// call counters (assertions on the batched-over-N+1 shape).
	presignBatchCalls int
	fetchBatchCalls   int
	publishCalls      int
	readCalls         int

	// putFailPaths: GCS PUT to any of these object paths returns 500 (Ship
	// PUT-failure surfacing test).
	putFailPaths map[string]struct{}
}

func newFakeSegmentBackend(t *testing.T) *fakeSegmentBackend {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	b := &fakeSegmentBackend{
		priv:         priv,
		objects:      map[string][]byte{},
		manifests:    map[string][]manifestDigest{},
		putFailPaths: map[string]struct{}{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/gcs/", b.handleGCS)
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

// mintObjectPath mirrors the agent's account-scoped, content-addressed key:
// segments/acct/<gt>/<name>/<format>/<hash>.seg.
func mintObjectPath(gt, name, format, hash string) string {
	return fmt.Sprintf("segments/acct/%s/%s/%s/%s.seg", gt, name, format, hash)
}

// signedURL encodes the object path into a GCS URL the httptest server decodes back.
func (b *fakeSegmentBackend) signedURL(objectPath string) string {
	return b.srv.URL + "/gcs/" + base64.RawURLEncoding.EncodeToString([]byte(objectPath))
}

// SegmentControlJSON makes the backend a segmentControlTransport, routing every T1
// segment control path.
func (b *fakeSegmentBackend) SegmentControlJSON(_ context.Context, path string, body []byte) ([]byte, error) {
	switch path {
	case "presign-batch":
		return b.handlePresignBatch(body)
	case "fetch-batch":
		return b.handleFetchBatch(body)
	case "manifest/publish":
		return b.handleManifestPublish(body)
	case "manifest/read":
		return b.handleManifestRead(body)
	default:
		return nil, fmt.Errorf("fake segment backend: unexpected control path %q", path)
	}
}

func (b *fakeSegmentBackend) handlePresignBatch(body []byte) ([]byte, error) {
	var req segmentPresignBatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.presignBatchCalls++
	b.mu.Unlock()
	resp := segmentPresignBatchResponse{Chunks: make([]presignResponse, 0, len(req.Chunks))}
	for _, ch := range req.Chunks {
		objectPath := mintObjectPath(req.GraphType, req.Name, req.Format, ch.ContentHash)
		resp.Chunks = append(resp.Chunks, presignResponse{
			UploadURL:      b.signedURL(objectPath),
			ObjectPath:     objectPath,
			AgentPublicKey: pemFromPublicKey(&b.priv.PublicKey),
			Expiry:         "2099-01-01T00:00:00Z",
		})
	}
	return json.Marshal(resp)
}

// handleFetchBatch mirrors the agent fetch: for each object path, HEAD the object,
// RSA-unwrap the DEK from its envelope header, and return download_url + base64 DEK.
// A missing object yields a per-element ok:false not_found (siblings unaffected).
func (b *fakeSegmentBackend) handleFetchBatch(body []byte) ([]byte, error) {
	var req segmentFetchBatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.fetchBatchCalls++
	b.mu.Unlock()
	resp := segmentFetchBatchResponse{Results: make([]segmentFetchElementResult, 0, len(req.Chunks))}
	for _, ch := range req.Chunks {
		b.mu.Lock()
		blob, ok := b.objects[ch.ObjectPath]
		b.mu.Unlock()
		if !ok {
			resp.Results = append(resp.Results, segmentFetchElementResult{OK: false, Error: "not_found", ObjectPath: ch.ObjectPath})
			continue
		}
		dek, err := unwrapDEKFromBlob(b.priv, blob)
		if err != nil {
			resp.Results = append(resp.Results, segmentFetchElementResult{OK: false, Error: "decrypt_failed", ObjectPath: ch.ObjectPath})
			continue
		}
		resp.Results = append(resp.Results, segmentFetchElementResult{
			OK:          true,
			DownloadURL: b.signedURL(ch.ObjectPath),
			DEK:         base64.StdEncoding.EncodeToString(dek),
			ObjectPath:  ch.ObjectPath,
		})
	}
	return json.Marshal(resp)
}

// handleManifestPublish HEAD-verifies every digest's blob is present (409 {missing}
// otherwise) then stores the manifest for the (gt,name,format).
func (b *fakeSegmentBackend) handleManifestPublish(body []byte) ([]byte, error) {
	var req manifestPublishRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.publishCalls++
	var missing []string
	for _, d := range req.Digests {
		objectPath := mintObjectPath(req.GraphType, req.Name, req.Format, d.ContentHash)
		if _, ok := b.objects[objectPath]; !ok {
			missing = append(missing, d.ContentHash)
		}
	}
	if len(missing) == 0 {
		b.manifests[manifestKey(req.GraphType, req.Name, req.Format)] = append([]manifestDigest(nil), req.Digests...)
	}
	b.mu.Unlock()
	if len(missing) > 0 {
		// Mirror the real transport: the agent returns HTTP 409 on genuine absence,
		// which SegmentControlJSON surfaces as a non-2xx *auth.SyncHTTPError whose
		// Body carries the missing hashes.
		raw, err := json.Marshal(manifestPublishResponse{OK: false, Missing: missing})
		if err != nil {
			return nil, err
		}
		return nil, &auth.SyncHTTPError{Path: "/v1/segments/manifest/publish", StatusCode: http.StatusConflict, Body: string(raw)}
	}
	return json.Marshal(manifestPublishResponse{OK: true})
}

func (b *fakeSegmentBackend) handleManifestRead(body []byte) ([]byte, error) {
	var req manifestReadRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.readCalls++
	digests, found := b.manifests[manifestKey(req.GraphType, req.Name, req.Format)]
	b.mu.Unlock()
	if !found {
		return json.Marshal(manifestReadResponse{Found: false})
	}
	out := make([]manifestReadDigest, len(digests))
	for i, d := range digests {
		out[i] = manifestReadDigest{
			ContentHash: d.ContentHash,
			DocCount:    d.DocCount,
			ObjectPath:  mintObjectPath(req.GraphType, req.Name, req.Format, d.ContentHash),
		}
	}
	return json.Marshal(manifestReadResponse{Found: true, Format: req.Format, Digests: out})
}

func (b *fakeSegmentBackend) handleGCS(w http.ResponseWriter, r *http.Request) {
	seg := lastPathSegment(r.URL.Path)
	pathBytes, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		http.Error(w, "bad object segment", http.StatusBadRequest)
		return
	}
	key := string(pathBytes)
	switch r.Method {
	case http.MethodPut:
		b.mu.Lock()
		_, fail := b.putFailPaths[key]
		b.mu.Unlock()
		if fail {
			http.Error(w, "injected put failure", http.StatusInternalServerError)
			return
		}
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

// failPUTForHash makes the GCS PUT for the object of (gt,name,format,hash) return 500.
func (b *fakeSegmentBackend) failPUTForHash(gt, name, format, hash string) {
	b.mu.Lock()
	b.putFailPaths[mintObjectPath(gt, name, format, hash)] = struct{}{}
	b.mu.Unlock()
}

func (b *fakeSegmentBackend) objectCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// seedManifest stores a published manifest directly (bypassing the publish handler's
// HEAD-verify), so List/Fetch tests can set up a manifest without depending on the
// PublishManifest impl status. digests is content_hash -> doc_count.
func (b *fakeSegmentBackend) seedManifest(gt, name, format string, digests map[string]int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]manifestDigest, 0, len(digests))
	for h, dc := range digests {
		out = append(out, manifestDigest{ContentHash: h, DocCount: dc})
	}
	b.manifests[manifestKey(gt, name, format)] = out
}

// storedPlaintextForHash decrypts the stored (sealed) object for a hash back to its
// plaintext under the Push AAD — proving the object round-trips.
func (b *fakeSegmentBackend) storedPlaintextForHash(t *testing.T, gt, name, format, hash string) []byte {
	t.Helper()
	objectPath := mintObjectPath(gt, name, format, hash)
	b.mu.Lock()
	blob, ok := b.objects[objectPath]
	b.mu.Unlock()
	if !ok {
		t.Fatalf("no stored object for hash %q", hash)
	}
	dek, err := unwrapDEKFromBlob(b.priv, blob)
	if err != nil {
		t.Fatalf("unwrap DEK: %v", err)
	}
	plain, err := syncgcs.OpenPushObject(blob, dek, objectPath)
	if err != nil {
		t.Fatalf("OpenPushObject: %v", err)
	}
	return plain
}

// --- helpers ---

func manifestKey(gt, name, format string) string { return gt + "/" + name + "/" + format }

func lastPathSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func pemFromPublicKey(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// unwrapDEKFromBlob parses the PUSH envelope header
// [u32 wrappedDEKLen][wrapped-DEK] and RSA-OAEP-SHA256 unwraps the DEK, exactly as
// the agent fetch handler's readWrappedDEK + KMS unwrap do.
func unwrapDEKFromBlob(priv *rsa.PrivateKey, blob []byte) ([]byte, error) {
	if len(blob) < syncgcs.EnvelopeWrappedDEKLenSize {
		return nil, fmt.Errorf("blob too short for length prefix")
	}
	wrappedLen := int(binary.BigEndian.Uint32(blob[:syncgcs.EnvelopeWrappedDEKLenSize]))
	end := syncgcs.EnvelopeWrappedDEKLenSize + wrappedLen
	if wrappedLen <= 0 || end > len(blob) {
		return nil, fmt.Errorf("bad wrapped-DEK length %d", wrappedLen)
	}
	return rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, blob[syncgcs.EnvelopeWrappedDEKLenSize:end], nil)
}
