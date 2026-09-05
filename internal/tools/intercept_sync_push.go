// SPDX-License-Identifier: Apache-2.0

// intercept_sync_push.go holds the PUSH half of the sync intercept: the presign/confirm
// control-plane DTOs, the GCS object content-type, the Exporter seam that reads the LOCAL
// graph, the push flow itself, and its caller-actionable error wrapping. The job-status
// half of the flow — the DTOs, the bounded poll and its terminal errors — is in
// sync_job_poll.go.
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
// confirm, which ENQUEUES the ingest and answers 202 with a job id. The agent's
// download, decrypt and ingest run behind that answer, so the push is not
// finished until pollSyncJob (sync_job_poll.go) sees the job reach a terminal
// state. Only the small presign/confirm/job-status control requests cross
// Cloudflare; the bulk ciphertext goes direct to GCS. The DEK lives only inside
// SealEnvelope and is discarded with the ciphertext buffer on return. Transport
// errors are wrapped for caller-actionable login guidance; a failed INGEST
// carries the gateway's own reason and is surfaced verbatim.
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

	// (4) Confirm: the agent validates and ENQUEUES the ingest, then answers 202
	// with a job id. The download, decrypt, bomb-check and ingest run behind
	// that answer — confirm returning is not the push landing.
	confirmReqBody, err := json.Marshal(syncConfirmRequest{
		ObjectPath: presign.ObjectPath,
		GraphType:  graph,
		Name:       name,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("sync push: marshal confirm request: %v", err))
	}
	confirmRaw, err := transport.SyncControlJSON(ctx, "confirm", confirmReqBody)
	if err != nil {
		return errorResult(wrapPushErr(graph, name, err))
	}
	var confirm syncConfirmResponse
	if err := json.Unmarshal(confirmRaw, &confirm); err != nil {
		return errorResult(fmt.Sprintf("sync push: decode confirm response: %v", err))
	}
	if confirm.JobID == "" {
		// Without a job id there is nothing to poll and no way to learn whether
		// the ingest landed. Reporting success here is exactly the failure this
		// whole change exists to remove.
		return errorResult(fmt.Sprintf(
			"sync push %s/%s: confirm returned no job id, so the ingest's outcome cannot be observed",
			graph, name))
	}
	// The state confirm reports is READ, not just decoded. The wire says a
	// confirm answers in_progress; a terminal value is still coherent (a job
	// that finished before this line ran), and the poll below settles either
	// case from the job record, which is the authority. A value outside the
	// three is a wire this build does not understand, and bad input errors here
	// rather than being ignored on the way to a poll whose answer would be
	// interpreted by the same misunderstanding.
	switch confirm.State {
	case syncJobStateInProgress, syncJobStateComplete, syncJobStateFailed:
	default:
		return errorResult(fmt.Sprintf(
			"sync push %s/%s: confirm reported an unrecognized job state %q (expected %s, %s or %s)",
			graph, name, confirm.State,
			syncJobStateInProgress, syncJobStateComplete, syncJobStateFailed))
	}

	// (5) Poll the job until it completes, fails, or the poll deadline expires.
	// The poll retries only what asking again could fix, so what arrives here is
	// a settled outcome. The poll's OWN errors — an unknown job, a refused
	// state, the deadline, a canceled wait — already carry the whole
	// operator-facing sentence and are surfaced verbatim. Everything else is a
	// transport refusal the poll declined to retry, and those go through
	// wrapPushErr, whose 401/403 login guidance is exactly the right advice for
	// a credential that expired mid-ingest.
	if err := pollSyncJob(ctx, transport, confirm.JobID, graph, name); err != nil {
		if _, ok := errors.AsType[syncJobError](err); ok {
			return errorResult(fmt.Sprintf("sync push %s/%s: %v", graph, name, err))
		}
		return errorResult(wrapPushErr(graph, name, err))
	}
	uploadDur := time.Since(uploadStart)

	return textResult(fmt.Sprintf("pushed %s/%s (%d bytes; serialize=%s upload+ingest=%s; job=%s)",
		graph, name, len(body),
		serializeDur.Round(time.Millisecond), uploadDur.Round(time.Millisecond), confirm.JobID))
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
