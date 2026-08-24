// SPDX-License-Identifier: Apache-2.0

// intercept_sync_push.go holds the PUSH half of the sync intercept: the presign/confirm
// control-plane DTOs, the GCS object content-type, the Exporter seam that reads the LOCAL
// graph, the push flow itself, and its caller-actionable error wrapping.
//
// The split is by DIRECTION, which is the seam intercept_sync.go's own doc already draws:
// "push reads local + writes cloud; pull reads cloud + writes local". It was made when the
// unchanged-graph pull short-circuit pushed intercept_sync.go past the 500-line cap. PUSH
// is the half that moved, deliberately: the pull flow stays in intercept_sync.go so the
// watermark ordering gate that greps that file for the Save-after-apply sequence keeps
// reading the code it was written against. Every declaration below moved VERBATIM, with no
// behaviour change of any kind.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// syncObjectContentType is the content-type the GCS PUT sends, matching what the
// agent presign signs the upload URL with (a Bearer header is NOT sent — that
// would break the GCS V4 signature; see syncgcs.PutObject).
const syncObjectContentType = "application/octet-stream"

// syncPresignRequest is the body of POST /v1/sync/presign.
type syncPresignRequest struct {
	GraphType string `json:"graph_type"`
	Name      string `json:"name"`
}

// syncPresignResponse is the response of POST /v1/sync/presign.
type syncPresignResponse struct {
	UploadURL      string `json:"upload_url"`
	ObjectPath     string `json:"object_path"`
	AgentPublicKey string `json:"agent_public_key"`
	Expiry         string `json:"expiry"`
}

// syncConfirmRequest is the body of POST /v1/sync/confirm.
type syncConfirmRequest struct {
	ObjectPath string `json:"object_path"`
	GraphType  string `json:"graph_type"`
	Name       string `json:"name"`
}

// Exporter is the narrow ExportGraph-RPC seam the push intercept drives. The
// production graphClientCaller implements it (Call + Execute + Index + ... +
// ExportGraph); tests inject a recording fake. Mirrors Indexer — type-asserted
// from the GraphCaller so the Call-only tools.GraphCaller interface stays
// unwidened.
type Exporter interface {
	ExportGraph(ctx context.Context, req *knowledgev1.ExportGraphRequest) (*knowledgev1.ExportGraphResponse, error)
}

// pushGraph serializes the LOCAL (graph, name) bytes via the ExportGraph RPC,
// then routes the bulk bytes to Fulminate Cloud through GCS (off Cloudflare's
// ~100 MB body cap): it requests a presigned PUT + the agent public key via the
// Bearer-authenticated /v1/sync control channel, asymmetric-envelope-encrypts the
// bytes (a fresh per-request DEK, AES-256-GCM, RSA-OAEP-SHA256-wrapped to the
// agent key), PUTs the ciphertext straight to GCS with NO auth header, then calls
// confirm so the agent downloads, decrypts, and ingests it. Only the small
// presign/confirm control requests cross Cloudflare; the bulk ciphertext goes
// direct to GCS. The DEK lives only inside SealEnvelope and is discarded with the
// ciphertext buffer on return. Errors are wrapped for caller-actionable login
// guidance.
func pushGraph(
	ctx context.Context,
	exp Exporter,
	transport *auth.Transport,
	graph, name string,
) kgtools.ToolResult {
	serializeStart := time.Now()
	resp, err := exp.ExportGraph(ctx, &knowledgev1.ExportGraphRequest{
		Target: manageGraphSelector(graph, name),
	})
	if err != nil {
		return errorResult(fmt.Sprintf("sync push: export %s/%s: %v", graph, name, err))
	}
	serializeDur := time.Since(serializeStart)
	body := resp.GetGraphBytes()

	uploadStart := time.Now()

	// (1) Request a presigned GCS PUT URL + the agent public key.
	presignReqBody, err := json.Marshal(syncPresignRequest{GraphType: graph, Name: name})
	if err != nil {
		return errorResult(fmt.Sprintf("sync push: marshal presign request: %v", err))
	}
	presignRaw, err := transport.SyncControlJSON(ctx, "presign", presignReqBody)
	if err != nil {
		return errorResult(wrapPushErr(graph, name, err))
	}
	var presign syncPresignResponse
	if err := json.Unmarshal(presignRaw, &presign); err != nil {
		return errorResult(fmt.Sprintf("sync push: decode presign response: %v", err))
	}

	// (2) Asymmetric-envelope-encrypt the serialized graph; the DEK is wrapped to
	// the agent public key and never leaves SealEnvelope.
	envelope, err := syncgcs.SealEnvelope(body, presign.AgentPublicKey, presign.ObjectPath)
	if err != nil {
		return errorResult(fmt.Sprintf("sync push: encrypt %s/%s: %v", graph, name, err))
	}

	// (3) PUT the ciphertext straight to GCS with NO auth header (octet-stream,
	// matching the content-type the presign signed).
	if err := syncgcs.PutObject(ctx, presign.UploadURL, envelope, syncObjectContentType); err != nil {
		return errorResult(fmt.Sprintf("sync push: upload %s/%s to GCS: %v", graph, name, err))
	}

	// (4) Confirm: the agent downloads, decrypts, bomb-checks, and ingests it.
	confirmReqBody, err := json.Marshal(syncConfirmRequest{
		ObjectPath: presign.ObjectPath,
		GraphType:  graph,
		Name:       name,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("sync push: marshal confirm request: %v", err))
	}
	if _, err := transport.SyncControlJSON(ctx, "confirm", confirmReqBody); err != nil {
		return errorResult(wrapPushErr(graph, name, err))
	}
	uploadDur := time.Since(uploadStart)

	return textResult(fmt.Sprintf("pushed %s/%s (%d bytes; serialize=%s upload=%s)",
		graph, name, len(body),
		serializeDur.Round(time.Millisecond), uploadDur.Round(time.Millisecond)))
}

// exporterSeam upgrades deps.LocalGraphCaller() to the Exporter seam, or
// returns a typed error so the missing seam is loud (degraded headless mode).
// Sync push reads bytes from the LOCAL graph (the destination is cloud via
// the auth.Transport), so the routing-aware GraphCaller would be wrong here —
// the source must always be local. Mirrors manageIndexer in shape.
func exporterSeam(deps ClientDeps) (Exporter, error) {
	gc := deps.LocalGraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("local graph caller unavailable — no local server is wired (cloud-first user without `knowledge install`)")
	}
	exp, ok := gc.(Exporter)
	if !ok {
		return nil, fmt.Errorf("sync requires an ExportGraph-capable graph client")
	}
	return exp, nil
}

// wrapPushErr produces a caller-actionable message for common auth failure modes
// on top of the underlying transport error. 401 means the refresh flow could not
// re-auth; 403 usually means the OAuth session lacks the `sync` scope.
// auth.ErrNotFound surfaces as "not logged in". Other errors are surfaced
// verbatim. Ported from the legacy server-side wrapSyncErr (engine_sync.go) so
// the chat-visible login guidance survives the move to the client.
func wrapPushErr(graph, name string, err error) string {
	if errors.Is(err, auth.ErrNotFound) {
		return fmt.Sprintf("sync push %s/%s: not logged in — run 'knowledge login' to authenticate",
			graph, name)
	}
	if se, ok := errors.AsType[*auth.SyncHTTPError](err); ok {
		switch se.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Sprintf("sync push %s/%s: authentication failed — run 'knowledge login' to refresh credentials (HTTP 401)",
				graph, name)
		case http.StatusForbidden:
			return fmt.Sprintf("sync push %s/%s: server rejected request — your login may lack the 'sync' scope (HTTP 403)",
				graph, name)
		}
	}
	return fmt.Sprintf("sync push %s/%s: %v", graph, name, err)
}
