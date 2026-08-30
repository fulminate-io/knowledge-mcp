// SPDX-License-Identifier: Apache-2.0

// Package transcriptsync is the standalone, daemon-independent engine behind the
// `knowledge transcript-upload` CLI subcommand. It ships the local CLI transcript
// corpus to the agent over the existing presigned-direct-to-GCS sync path with
// mode=transcript: per-session whole-file-incremental, ONE parquet object per
// changed session, gated once-per-batch on the per-account
// transcript_collection_enabled consent flag.
//
// It reuses the graph-sync library seams verbatim — syncgcs.SealEnvelope /
// syncgcs.PutObject for the crypto + GCS transfer, and auth.Transport's
// SyncControlJSON for the small JSON control requests (presign-batch /
// confirm-batch / consent) — and parses the corpus CLIENT-SIDE via the transcripts
// package, then CONVERTS each session to a bounded parquet locally (temp-file
// row-group flush) and ships the sealed parquet object rather than raw transcript
// bytes. It touches the knowledge graph not at all and needs no LocalGraphCaller,
// MCP server, or daemon.
package transcriptsync

// ModeTranscript is the /v1/sync mode discriminator the agent routes on: a presign /
// confirm carrying mode=transcript lands the sealed session parquet in the per-user
// transcript corpus rather than the graph-sync object path. It rides in the request
// bodies; the /v1/sync/ URL prefix is unchanged.
const ModeTranscript = "transcript"

// presignResponse is the agent's presign reply. It is the verbatim field + JSON
// tag mirror of the graph-sync syncPresignResponse
// (cmd/knowledge/internal/tools/intercept_sync.go) so the agent's presign reply
// decodes identically across both modes: a presigned GCS PUT URL, the
// agent-minted object path (bound into the envelope AAD), the agent's RSA public
// key the DEK is wrapped to, and an expiry timestamp.
type presignResponse struct {
	UploadURL      string `json:"upload_url"`
	ObjectPath     string `json:"object_path"`
	AgentPublicKey string `json:"agent_public_key"`
	Expiry         string `json:"expiry"`
}

// =============================================================================
// Batch transcript control plane — hand-mirrored agent contract.
//
// The batch DTOs below collapse the per-chunk presign/confirm into one metered
// request per batch. They are the CROSS-MODULE MIRROR of the agent's sync_batch.go
// DTOs — field-for-field and json-tag-for-json-tag — the same way the single-mode
// DTOs above mirror the agent's single endpoints (knowledge must NOT import the
// agent's generated code; the wire JSON is the shared contract). Any change here is
// a wire-format change requiring a coordinated agent update.
//
// BATCH-SIZE CEILING CONTRACT (hand-mirrored, both sides): the agent caps a batch
// at maxBatchChunks=512 and 400s an over-cap batch. The client mirrors that const
// and CLAMPS Config.BatchSize down to it (clampBatchSize, run.go) so an over-cap
// misconfig can never make every batch 400 and permanently strand files. An unset
// BatchSize defaults to defaultBatchSize=32.
// =============================================================================

const (
	// maxBatchChunks is the per-batch object ceiling, MIRRORING the agent's
	// maxBatchChunks (sync_batch.go). Config.BatchSize is clamped to it.
	maxBatchChunks = 512

	// defaultBatchSize is the objects-per-batch used when Config.BatchSize is unset
	// (<=0). It is 32, not the graph-sync default: confirm now GCS-downloads +
	// KMS-decrypts + GCS-uploads each session parquet SYNCHRONOUSLY inside the
	// confirm-batch request (the old confirm returned fast — conversion was async),
	// so a smaller batch bounds the per-request wall-time under the agent's 10s
	// SERVER_WRITE_TIMEOUT for the everyday KB–MB parquet payload.
	defaultBatchSize = 32
)

// presignBatchChunk is one session's identity tuple in a batch presign request. The
// object identity is now just Source+Session (one parquet object per session); its
// json tags mirror the agent's per-object presign fields. Mode is top-level on the
// batch envelope, not per object.
type presignBatchChunk struct {
	Source  string `json:"source"`
	Session string `json:"session"`
}

// presignBatchRequest is the body of POST /v1/sync/presign-batch: a top-level mode
// discriminator plus an ordered array of object identities. The response Chunks pair
// to these BY INDEX.
type presignBatchRequest struct {
	Mode   string              `json:"mode"`
	Chunks []presignBatchChunk `json:"chunks"`
}

// presignBatchResponse is the agent's batch presign reply: an ordered array of
// per-chunk presign results, index-parallel to the request Chunks. The per-element
// type is the EXISTING presignResponse verbatim (every element carries the SAME
// agent_public_key).
type presignBatchResponse struct {
	Chunks []presignResponse `json:"chunks"`
}

// confirmBatchChunk is one session's confirm identity in a batch confirm request. Its
// json tags mirror the sync backend's per-object confirm fields (the backend-minted
// ObjectPath plus the identity tuple); Mode is top-level on the batch envelope, not per
// object. Rollup carries the per-session usage-rollup payload computed client-side at
// upload time; it is a pointer with NO omitempty (mirrors the frozen backend field) so a
// session with an empty rollup still ships the key rather than dropping it. Identity for
// every rollup row is stamped by the sync backend from this chunk's Source/Session +
// bearer context — the rows themselves are identity-free.
type confirmBatchChunk struct {
	ObjectPath string         `json:"object_path"`
	Source     string         `json:"source"`
	Session    string         `json:"session"`
	Rollup     *rollupPayload `json:"rollup"`
}

// confirmBatchRequest is the body of POST /v1/sync/confirm-batch: a top-level mode
// discriminator plus an ordered array of object confirms.
type confirmBatchRequest struct {
	Mode   string              `json:"mode"`
	Chunks []confirmBatchChunk `json:"chunks"`
}

// confirmElementResult is the per-element outcome in a batch confirm response,
// index-parallel to the submitted Chunks. OK=true means the chunk's part landed.
type confirmElementResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// confirmBatchResponse is the agent's batch confirm reply: a request-order-parallel
// array of per-element results.
type confirmBatchResponse struct {
	Results []confirmElementResult `json:"results"`
}

// =============================================================================
// Usage-rollup wire contract — hand-mirrored, FROZEN v1.
//
// The rollup DTOs below are the client half of the per-session usage-rollup payload
// that rides on the existing confirm-batch POST (confirmBatchChunk.Rollup). They are a
// verbatim field + json-tag MIRROR of the sync backend's frozen rollup contract — the
// knowledge client computes the rows in pure Go at upload time (rollup.go) and the
// backend stamps identity + upserts them. Knowledge must NOT import the backend's
// generated code; the wire JSON is the shared contract, so any change here is a
// wire-format change requiring a coordinated backend update.
//
// IDENTITY IS NEVER IN THE PAYLOAD: no row type declares account_id / user_id /
// session_id / source. The backend derives account+user from the bearer-validated
// confirm context and session+source from the enclosing chunk's Source/Session.
// =============================================================================

const (
	// rollupSchemaVersion is the frozen rollup contract version (hand-mirrored with the
	// sync backend). It is stamped onto every shipped rollupPayload and persisted into
	// the per-session watermark; a stored version below this forces a whole-session
	// re-derive + re-ship (the schema-bump backfill).
	rollupSchemaVersion = 2

	// rollupSubagentIdleGapMs is the inter-event gap at or above which an agent lane is
	// considered idle: gaps strictly BELOW it count as working time, gaps at or above it
	// are pauses and are dropped. Hand-mirrored from the daemon-local analyzer's
	// subagentIdleGapMs (transcriptanalytics/detectors_schema.go) — the two consts MUST
	// move together, and a gate asserts they are numerically equal.
	rollupSubagentIdleGapMs int64 = 600_000 // 10m in ms

	// rollupIdleGuardCeilingMs is the last-resort idle guard for tool-execution time: a
	// span longer than 2h is assumed to straddle a paused/resumed session and is EXCLUDED
	// from the trustworthy-time metrics (it is NOT a cap on legitimate long runs). Hand-
	// mirrored from the sync backend's idle guard AND kept in lockstep with the daemon-
	// local analyzer's idleGuardCeilingMs (transcriptanalytics/detectors_schema.go) — the
	// two consts MUST move together.
	rollupIdleGuardCeilingMs int64 = 7_200_000 // 2h in ms

	// rollupBucketMaxExp is the clamp on the log2 tool-latency bucket exponent (bucket b
	// covers [2^b, 2^(b+1)) ms). Hand-mirrored from the sync backend's bucket scheme.
	rollupBucketMaxExp = 31
)

// rollupPayload is the per-session usage rollup shipped on confirm. SchemaVersion pins
// the frozen contract; the five row slices are the aggregated row kinds the backend
// upserts. Hand-mirrored, identity-free.
type rollupPayload struct {
	SchemaVersion     int              `json:"schema_version"`
	Session           sessionScalars   `json:"session"`
	Facts             []factRow        `json:"facts"`
	LatencyHist       []latencyHistRow `json:"latency_hist"`
	SlowCalls         []slowCallRow    `json:"slow_calls"`
	DuplicateCommands []duplicateRow   `json:"duplicate_commands"`
}

// sessionScalars are the per-session single-row aggregates. AgentChainDepth is the
// distinct-subagent count proxy (NOT true spawn depth). first/last_record_ts are RFC3339
// renderings of the min/max record timestamp; duration_ms is the RAW (un-idle-guarded)
// sum. json tags == snake_case field names; identity-free.
type sessionScalars struct {
	Project               string `json:"project"`
	FirstRecordTS         string `json:"first_record_ts"`
	LastRecordTS          string `json:"last_record_ts"`
	AgentChainDepth       int64  `json:"agent_chain_depth"`
	RecordCount           int64  `json:"record_count"`
	InputTokens           int64  `json:"input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
	CacheReadTokens       int64  `json:"cache_read_tokens"`
	CacheCreationTokens   int64  `json:"cache_creation_tokens"`
	CacheCreation1hTokens int64  `json:"cache_creation_1h_tokens"`
	CacheCreation5mTokens int64  `json:"cache_creation_5m_tokens"`
	DurationMs            int64  `json:"duration_ms"`
	ErrorCount            int64  `json:"error_count"`
	APIErrorCount         int64  `json:"api_error_count"`
	InterruptedCount      int64  `json:"interrupted_count"`
	WebSearchCount        int64  `json:"web_search_count"`
	WebFetchCount         int64  `json:"web_fetch_count"`
}

// factRow is one row at the (day × full dimension tuple) grain. DurationMs is the RAW
// sum over ALL rows in the grain; TrustworthyDurationMs sums only idle-guarded rows.
// Synthetic-model and is_meta rows are shipped VERBATIM (the backend filters at read
// time). json tags == snake_case field names; identity-free.
type factRow struct {
	Day                   string `json:"day"`
	Model                 string `json:"model"`
	ToolName              string `json:"tool_name"`
	Project               string `json:"project"`
	SubagentType          string `json:"subagent_type"`
	AgentID               string `json:"agent_id"`
	IsSidechain           bool   `json:"is_sidechain"`
	IsMeta                bool   `json:"is_meta"`
	MCPServer             string `json:"mcp_server"`
	MCPTool               string `json:"mcp_tool"`
	Skill                 string `json:"skill"`
	ServiceTier           string `json:"service_tier"`
	StopReason            string `json:"stop_reason"`
	RecordCount           int64  `json:"record_count"`
	InputTokens           int64  `json:"input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
	CacheReadTokens       int64  `json:"cache_read_tokens"`
	CacheCreationTokens   int64  `json:"cache_creation_tokens"`
	CacheCreation1hTokens int64  `json:"cache_creation_1h_tokens"`
	CacheCreation5mTokens int64  `json:"cache_creation_5m_tokens"`
	DurationMs            int64  `json:"duration_ms"`
	TrustworthyDurationMs int64  `json:"trustworthy_duration_ms"`
	APIErrorCount         int64  `json:"api_error_count"`
	InterruptedCount      int64  `json:"interrupted_count"`
	ErrorCount            int64  `json:"error_count"`
	WebSearchCount        int64  `json:"web_search_count"`
	WebFetchCount         int64  `json:"web_fetch_count"`
	MinRecordTS           string `json:"min_record_ts"`
	MaxRecordTS           string `json:"max_record_ts"`

	// The two per-agent active measures, denormalized onto every qualifying row of the
	// agent's grain (the reader takes MAX at each field's own grain and NEVER SUMs).
	//
	// active_ms is the WHOLE-LIFE per-(session, agent_id) active total and is the only
	// field carrying an exact daemon-parity claim.
	//
	// day_active_ms is the per-(session, agent_id, day) bucket and is a documented LOWER
	// BOUND: a gap spanning midnight falls in neither day's instant list and is dropped
	// entirely.
	//
	// nil means UNMEASURED and is never the same statement as a measured 0. A measured 0
	// is real and common (any agent-day with fewer than two instants). Neither field
	// carries omitempty: a nil must marshal as an explicit null so the key stays present
	// on the wire, which is what keeps unmeasured distinguishable from a v1 payload.
	ActiveMs    *int64 `json:"active_ms"`
	DayActiveMs *int64 `json:"day_active_ms"`
}

// latencyHistRow is a sparse tool-latency histogram bin: only trustworthy tool rows
// (tool_name != "") are bucketed. IsMeta joins the grain (frozen contract). json tags ==
// snake_case field names; identity-free.
type latencyHistRow struct {
	Day         string `json:"day"`
	ToolName    string `json:"tool_name"`
	Model       string `json:"model"`
	Project     string `json:"project"`
	IsSidechain bool   `json:"is_sidechain"`
	IsMeta      bool   `json:"is_meta"`
	Bucket      int    `json:"bucket"`
	CallCount   int64  `json:"call_count"`
}

// slowCallRow is one of the per-(session, tool_name) top-100 slowest trustworthy tool
// calls. It is a per-row record (NOT grouped), so it carries NO is_meta field. json tags
// == snake_case field names; identity-free.
type slowCallRow struct {
	Day              string `json:"day"`
	ToolName         string `json:"tool_name"`
	Model            string `json:"model"`
	Project          string `json:"project"`
	IsSidechain      bool   `json:"is_sidechain"`
	MCPServer        string `json:"mcp_server"`
	MCPTool          string `json:"mcp_tool"`
	DurationMs       int64  `json:"duration_ms"`
	RecordTS         string `json:"record_ts"`
	ToolInputPreview string `json:"tool_input_preview"`
}

// duplicateRow is a FINE-grain duplicate-command row (grain includes model/project/
// is_sidechain/is_meta/day so the backend can filter those columns row-wise before
// re-aggregating GROUP BY session,tool,hash). RunCount is the fine-grain count (NOT the
// session total); WastedDurationMs sums trustworthy rows only. A fine row is shipped only
// when its parent (tool_name, tool_input_hash) SESSION-TOTAL run_count > 1 — the emission
// gate lives in the compute (rollup.go), not this struct. json tags == snake_case field
// names; identity-free.
type duplicateRow struct {
	Day              string `json:"day"`
	ToolName         string `json:"tool_name"`
	ToolInputHash    string `json:"tool_input_hash"`
	Model            string `json:"model"`
	Project          string `json:"project"`
	IsSidechain      bool   `json:"is_sidechain"`
	IsMeta           bool   `json:"is_meta"`
	RunCount         int64  `json:"run_count"`
	WastedDurationMs int64  `json:"wasted_duration_ms"`
	SamplePreview    string `json:"sample_preview"`
	FirstRecordTS    string `json:"first_record_ts"`
}
