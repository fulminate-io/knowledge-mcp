// SPDX-License-Identifier: Apache-2.0

package bootstrap

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
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// segment_backend_fake_test.go is the bootstrap-local copy of segmentdist's
// fakeSegmentBackend (source_gcs_testserver_test.go): the GCS-agent segment control
// plane + GCS object store fake the logged-in cloud heal/reconcile/fastload fixtures
// drive through segmentdist.WithSegmentTransport. With the SegmentService deleted,
// the cloud segment source is the GCS-agent source, reached over the
// SegmentControlTransport (SegmentControlJSON) seam — NOT a connect SegmentService
// handler. Per AGENTS.md the two test doubles cannot share a Go package (the
// segmentdist one is package-private), so this is a faithful COPY, not an import;
// the wire JSON DTOs below are hand-mirrored against segmentdist/source_gcs_wire.go.

// --- hand-mirrored wire DTOs (segmentdist/source_gcs_wire.go) --------------

type segPresignChunk struct {
	ContentHash string `json:"content_hash"`
}
type segPresignBatchRequest struct {
	GraphType string            `json:"graph_type"`
	Name      string            `json:"name"`
	Format    string            `json:"format"`
	Chunks    []segPresignChunk `json:"chunks"`
}
type segPresignResponse struct {
	UploadURL      string `json:"upload_url"`
	ObjectPath     string `json:"object_path"`
	AgentPublicKey string `json:"agent_public_key"`
	Expiry         string `json:"expiry"`
}
type segPresignBatchResponse struct {
	Chunks []segPresignResponse `json:"chunks"`
}
type segFetchChunk struct {
	ObjectPath string `json:"object_path"`
}
type segFetchBatchRequest struct {
	Chunks []segFetchChunk `json:"chunks"`
}
type segFetchElementResult struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	DEK         string `json:"dek,omitempty"`
	ObjectPath  string `json:"object_path,omitempty"`
}
type segFetchBatchResponse struct {
	Results []segFetchElementResult `json:"results"`
}
type segManifestDigest struct {
	ContentHash string `json:"content_hash"`
	DocCount    int    `json:"doc_count"`
}
type segManifestPublishRequest struct {
	GraphType string              `json:"graph_type"`
	Name      string              `json:"name"`
	Format    string              `json:"format"`
	Digests   []segManifestDigest `json:"digests"`
}
type segManifestPublishResponse struct {
	OK      bool     `json:"ok"`
	Missing []string `json:"missing,omitempty"`
}
type segManifestReadRequest struct {
	GraphType string `json:"graph_type"`
	Name      string `json:"name"`
	Format    string `json:"format"`
}
type segManifestReadDigest struct {
	ContentHash string `json:"content_hash"`
	DocCount    int    `json:"doc_count"`
	ObjectPath  string `json:"object_path"`
}
type segManifestReadResponse struct {
	Found   bool                    `json:"found"`
	Format  string                  `json:"format"`
	Digests []segManifestReadDigest `json:"digests,omitempty"`
}

// fakeSegBackend plays BOTH roles of the GCS-agent segment flow: the agent control
// plane (SegmentControlJSON for presign-batch / fetch-batch / manifest publish+read,
// satisfying segmentdist.SegmentControlTransport in-process) AND GCS object storage
// (an httptest server the presigned PUT/GET URLs point at), with a real RSA-3072
// keypair for true envelope round-trips. A faithful copy of segmentdist's
// fakeSegmentBackend.
type fakeSegBackend struct {
	srv  *httptest.Server
	priv *rsa.PrivateKey

	mu        sync.Mutex
	objects   map[string][]byte
	manifests map[string][]segManifestDigest

	publishCalls int
	readCalls    int
	// publishByGraph / readByGraph split those totals per graph instance. A pass that
	// publishes for one graph and not another is invisible in a global count, and
	// "this graph was never published for" is exactly the claim the working-set
	// regression makes.
	publishByGraph map[string]int
	readByGraph    map[string]int
	// failReadAfterN, when > 0, makes the first N manifest/read calls succeed and
	// every read AFTER that return a transport error (the 524/down shape) — the GCS
	// analog of the deleted healSegmentService.failListAfterN, letting a fixture model
	// a server that answers the cheap presence probe but times out on the heal load.
	failReadAfterN int
}

func newFakeSegBackend(t *testing.T) *fakeSegBackend {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	b := &fakeSegBackend{
		priv:      priv,
		objects:   map[string][]byte{},
		manifests: map[string][]segManifestDigest{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/gcs/", b.handleGCS)
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

func segMintObjectPath(gt, name, format, hash string) string {
	return fmt.Sprintf("segments/acct/%s/%s/%s/%s.seg", gt, name, format, hash)
}

func segManifestKey(gt, name, format string) string { return gt + "/" + name + "/" + format }

func (b *fakeSegBackend) signedURL(objectPath string) string {
	return b.srv.URL + "/gcs/" + base64.RawURLEncoding.EncodeToString([]byte(objectPath))
}

// SegmentControlJSON makes the backend a segmentdist.SegmentControlTransport.
func (b *fakeSegBackend) SegmentControlJSON(_ context.Context, path string, body []byte) ([]byte, error) {
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

func (b *fakeSegBackend) handlePresignBatch(body []byte) ([]byte, error) {
	var req segPresignBatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	resp := segPresignBatchResponse{Chunks: make([]segPresignResponse, 0, len(req.Chunks))}
	for _, ch := range req.Chunks {
		objectPath := segMintObjectPath(req.GraphType, req.Name, req.Format, ch.ContentHash)
		resp.Chunks = append(resp.Chunks, segPresignResponse{
			UploadURL:      b.signedURL(objectPath),
			ObjectPath:     objectPath,
			AgentPublicKey: segPEMFromPublicKey(&b.priv.PublicKey),
			Expiry:         "2099-01-01T00:00:00Z",
		})
	}
	return json.Marshal(resp)
}

func (b *fakeSegBackend) handleFetchBatch(body []byte) ([]byte, error) {
	var req segFetchBatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	resp := segFetchBatchResponse{Results: make([]segFetchElementResult, 0, len(req.Chunks))}
	for _, ch := range req.Chunks {
		b.mu.Lock()
		blob, ok := b.objects[ch.ObjectPath]
		b.mu.Unlock()
		if !ok {
			resp.Results = append(resp.Results, segFetchElementResult{OK: false, Error: "not_found", ObjectPath: ch.ObjectPath})
			continue
		}
		dek, err := segUnwrapDEK(b.priv, blob)
		if err != nil {
			resp.Results = append(resp.Results, segFetchElementResult{OK: false, Error: "decrypt_failed", ObjectPath: ch.ObjectPath})
			continue
		}
		resp.Results = append(resp.Results, segFetchElementResult{
			OK:          true,
			DownloadURL: b.signedURL(ch.ObjectPath),
			DEK:         base64.StdEncoding.EncodeToString(dek),
			ObjectPath:  ch.ObjectPath,
		})
	}
	return json.Marshal(resp)
}

func (b *fakeSegBackend) handleManifestPublish(body []byte) ([]byte, error) {
	var req segManifestPublishRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.publishCalls++
	if b.publishByGraph == nil {
		b.publishByGraph = map[string]int{}
	}
	b.publishByGraph[req.GraphType+"/"+req.Name]++
	var missing []string
	for _, d := range req.Digests {
		objectPath := segMintObjectPath(req.GraphType, req.Name, req.Format, d.ContentHash)
		if _, ok := b.objects[objectPath]; !ok {
			missing = append(missing, d.ContentHash)
		}
	}
	if len(missing) == 0 {
		b.manifests[segManifestKey(req.GraphType, req.Name, req.Format)] = append([]segManifestDigest(nil), req.Digests...)
	}
	b.mu.Unlock()
	if len(missing) > 0 {
		raw, err := json.Marshal(segManifestPublishResponse{OK: false, Missing: missing})
		if err != nil {
			return nil, err
		}
		return nil, &auth.SyncHTTPError{Path: "/v1/segments/manifest/publish", StatusCode: http.StatusConflict, Body: string(raw)}
	}
	return json.Marshal(segManifestPublishResponse{OK: true})
}

func (b *fakeSegBackend) handleManifestRead(body []byte) ([]byte, error) {
	var req segManifestReadRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.readCalls++
	if b.readByGraph == nil {
		b.readByGraph = map[string]int{}
	}
	b.readByGraph[req.GraphType+"/"+req.Name]++
	failAfter := b.failReadAfterN
	reads := b.readCalls
	digests, found := b.manifests[segManifestKey(req.GraphType, req.Name, req.Format)]
	b.mu.Unlock()
	if failAfter > 0 && reads > failAfter {
		// The backend answered the cheap presence probes, now times out on the heal
		// load()'s manifest/read — the down/524 shape driving the probe-error arm.
		return nil, &auth.SyncHTTPError{Path: "/v1/segments/manifest/read", StatusCode: http.StatusServiceUnavailable, Body: "timeout"}
	}
	if !found {
		return json.Marshal(segManifestReadResponse{Found: false})
	}
	out := make([]segManifestReadDigest, len(digests))
	for i, d := range digests {
		out[i] = segManifestReadDigest{
			ContentHash: d.ContentHash,
			DocCount:    d.DocCount,
			ObjectPath:  segMintObjectPath(req.GraphType, req.Name, req.Format, d.ContentHash),
		}
	}
	return json.Marshal(segManifestReadResponse{Found: true, Format: req.Format, Digests: out})
}

func (b *fakeSegBackend) handleGCS(w http.ResponseWriter, r *http.Request) {
	seg := segLastPathSegment(r.URL.Path)
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

// seedManifest stores a published manifest directly (bypassing the publish handler's
// HEAD-verify), so coverage/presence probes can set up a manifest with real
// doc_counts WITHOUT shipping real blobs. digests is content_hash -> doc_count.
func (b *fakeSegBackend) seedManifest(gt, name, format string, digests []segManifestDigest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.manifests[segManifestKey(gt, name, format)] = append([]segManifestDigest(nil), digests...)
}

func (b *fakeSegBackend) transportBuilder() func() (segmentdist.SegmentControlTransport, error) {
	return func() (segmentdist.SegmentControlTransport, error) { return b, nil }
}

// --- crypto helpers (copied from segmentdist's testserver) ------------------

func segPEMFromPublicKey(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func segLastPathSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func segUnwrapDEK(priv *rsa.PrivateKey, blob []byte) ([]byte, error) {
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
