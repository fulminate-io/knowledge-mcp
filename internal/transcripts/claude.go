// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Claude transcripts are FLAT, self-contained records: every line carries its
// own top-level cwd/gitBranch/version/sessionId/uuid/parentUuid envelope plus a
// nested message. These structs decode the whole envelope because that is what
// the column parser needs, not just the tool-correlation fields.

// claudeEnvelope is the top-level claude transcript line. Only the fields the
// column parser reads are decoded; unknown fields are ignored.
//
// The block after RequestID is the enrichment envelope: subagent identity
// (agentId) and the within-file subagent-type carrier (attributionAgent, which
// equals the spawning Task's subagent_type on every record of that subagent's
// side conversation), MCP/skill attribution, and the meta/interrupt/api-error
// signals. durationMs is the CLI-native turn latency; the Row's duration_ms is
// DERIVED from record timestamps in ParseClaude (this field is decoded for
// completeness, not used as the Row value).
type claudeEnvelope struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	ParentUUID  string `json:"parentUuid"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	Version     string `json:"version"`
	SessionID   string `json:"sessionId"`
	RequestID   string `json:"requestId"`

	AgentID              string `json:"agentId"`
	IsMeta               bool   `json:"isMeta"`
	InterruptedMessageID string `json:"interruptedMessageId"`
	DurationMs           int64  `json:"durationMs"`
	AttributionAgent     string `json:"attributionAgent"`
	AttributionMCPServer string `json:"attributionMcpServer"`
	AttributionMCPTool   string `json:"attributionMcpTool"`
	AttributionSkill     string `json:"attributionSkill"`
	IsAPIErrorMessage    bool   `json:"isApiErrorMessage"`

	Message *claudeMsg `json:"message"`
}

// claudeMsg is the nested message object. Content is held as RawMessage because
// a user-typed message carries a plain string there while assistant/tool records
// carry an array of blocks — decoding lazily keeps a string-content record from
// failing the whole-line decode and dropping its envelope.
type claudeMsg struct {
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	Content    json.RawMessage `json:"content"`
	Usage      *claudeUsage    `json:"usage"`
	StopReason string          `json:"stop_reason"`
}

// claudeUsage is the assistant turn's token accounting. The four raw counts map
// 1:1 onto the Row token columns; nothing is folded or summed. The nested
// cache_creation split (ephemeral 1h/5m) and server_tool_use web counts are the
// enrichment sub-objects; service_tier records the pricing tier the turn billed
// at.
type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`

	CacheCreation struct {
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
	} `json:"cache_creation"`
	ServiceTier   string `json:"service_tier"`
	ServerToolUse struct {
		WebSearch int64 `json:"web_search_requests"`
		WebFetch  int64 `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
}

// claudeContentBlock is one content block: tool_use carries an id+name+input; a
// tool_result carries the tool_use_id it answers, an is_error flag and the
// content it returned. Input is the tool's argument payload (e.g. Bash
// {"command":...}), fingerprinted into tool_input_hash/preview for
// duplicate-command detection; Content is what the call handed back, sized into
// tool_result_bytes/images (claude_result_size.go).
type claudeContentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
}

// claudeBlocks decodes a message's content as a block array, tolerating the
// string-content shape (a user-typed message) by returning nil.
func claudeBlocks(raw json.RawMessage) []claudeContentBlock {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var blocks []claudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// ParseClaude column-extracts a claude transcript (flat self-contained records)
// into Rows. Each assistant record with usage yields a token Row carrying the
// four raw token counts + model; each tool_use block yields a tool Row whose
// IsError is set later by correlating its id to the matching tool_result's
// is_error in a following user record (the id-correlation shape of
// applyClaudeRecord, not its liveness logic). A record whose model is
// "<synthetic>" (claude's injected non-API turns) is skipped entirely. The scan
// is tolerant: blank/non-JSON lines and a half-written trailing line (an
// in-progress file) are skipped rather than erroring.
//
// Enrichment derived here (single scan pass + one O(rows) stamp pass):
//   - duration_ms: a tool Row measures its tool_result.ts − tool_use.ts; a token
//     Row measures assistant.ts − the most recent user-role record ts. NOTE: in
//     claude transcripts tool_result records ARE type=="user", so for a turn that
//     follows tool calls this is time since the last tool_result — a MODEL-LATENCY
//     proxy, NOT human-turn wall-clock. Subagent wall-time is deliberately a
//     query-side MAX−MIN(record_ts) GROUP BY agent_id aggregate, never stored here.
//   - subagent_type: a subagent conversation is its own file where attributionAgent
//     is a within-file constant (== the spawning Task's subagent_type). We capture
//     the first non-empty value and stamp every row after the scan; a main-agent
//     file carries no attributionAgent, so its rows keep an empty subagent_type.
func ParseClaude(r io.Reader) ([]Row, error) {
	scan := &claudeScan{toolRowByID: map[string]int{}}

	var offset int64
	sc := newJSONLScanner(r)
	for sc.Scan() {
		line := sc.Bytes()
		recordStart := offset
		// Advance to the next record's start. JSONL is LF-terminated, so the +1
		// re-adds the newline bufio.Scanner strips, giving the exact byte offset
		// of the NEXT record. This LF-terminated assumption holds for local CLI
		// output; KN-2's watermark inherits it.
		offset += int64(len(line)) + 1

		scan.record(line, recordStart)
	}
	if err := sc.Err(); err != nil {
		return scan.rows, err
	}
	// One O(rows) pass stamps the file-constant subagent type. A main-agent file
	// leaves subagentType empty, so its rows keep an empty subagent_type.
	if scan.subagentType != "" {
		for i := range scan.rows {
			scan.rows[i].SubagentType = scan.subagentType
		}
	}
	return scan.rows, nil
}

// claudeScan is the cross-record state of one ParseClaude pass.
type claudeScan struct {
	rows []Row
	// toolRowByID maps a tool_use id to the index of its tool Row so a later
	// tool_result can flip its is_error and stamp its duration_ms.
	toolRowByID map[string]int
	// lastUserTS is the timestamp of the most recent type=="user" record (which
	// includes tool_result records) — the anchor a following token Row measures
	// its model-latency proxy against.
	lastUserTS time.Time
	// subagentType is the file-constant attributionAgent, captured on first sight
	// and stamped onto every row after the scan.
	subagentType string
}

// record column-extracts one transcript line, appending its token/tool Rows and
// advancing the scan state. Blank, non-JSON, malformed (half-written trailing),
// message-less, and synthetic-model lines are skipped without error.
func (s *claudeScan) record(line []byte, recordStart int64) {
	if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
		return
	}
	var env claudeEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		// Malformed or half-written trailing line — skip, keep parsing.
		return
	}
	msg := env.Message
	if msg == nil {
		return
	}
	if msg.Model == "<synthetic>" {
		// Claude's injected synthetic turns are not real API calls — skip.
		return
	}

	// Capture the file-constant subagent type on first sight (stamped after the
	// scan by ParseClaude).
	if s.subagentType == "" && env.AttributionAgent != "" {
		s.subagentType = env.AttributionAgent
	}

	base := Row{
		Source:       SourceClaude,
		SessionID:    env.SessionID,
		Project:      env.Cwd,
		GitBranch:    env.GitBranch,
		RecordTS:     parseRecordTS(env.Timestamp),
		RecordType:   env.Type,
		CLIVersion:   env.Version,
		UUID:         env.UUID,
		ParentUUID:   env.ParentUUID,
		SourceOffset: recordStart,
		// Envelope-wide enrichment: present on every record, so stamp on base
		// and it flows to both the token Row and the tool Rows. subagent_type
		// (attributionAgent) is stamped in a post-scan pass — see above.
		IsSidechain: env.IsSidechain,
		AgentID:     env.AgentID,
		IsMeta:      env.IsMeta,
		Interrupted: env.InterruptedMessageID != "",
		IsAPIError:  env.IsAPIErrorMessage,
		MCPServer:   env.AttributionMCPServer,
		MCPTool:     env.AttributionMCPTool,
		Skill:       env.AttributionSkill,
	}

	// Token Row: an assistant turn with usage accounting.
	if env.Type == "assistant" && msg.Usage != nil {
		s.rows = append(s.rows, s.tokenRow(base, msg))
	}

	// Content blocks: tool_use issues a tool Row; tool_result flips IsError and
	// stamps duration_ms on the tool Row it answers.
	for _, b := range claudeBlocks(msg.Content) {
		s.contentBlock(base, msg, b)
	}

	// A user-role record (incl. tool_result carriers) advances the latency
	// anchor for the next assistant turn.
	if env.Type == "user" {
		s.lastUserTS = base.RecordTS
	}
}

// tokenRow builds the token Row for an assistant turn with usage accounting.
func (s *claudeScan) tokenRow(base Row, msg *claudeMsg) Row {
	ur := base
	ur.Model = msg.Model
	ur.InputTokens = msg.Usage.InputTokens
	ur.OutputTokens = msg.Usage.OutputTokens
	ur.CacheCreationTokens = msg.Usage.CacheCreationInputTokens
	ur.CacheReadTokens = msg.Usage.CacheReadInputTokens
	ur.CacheCreation1hTokens = msg.Usage.CacheCreation.Ephemeral1h
	ur.CacheCreation5mTokens = msg.Usage.CacheCreation.Ephemeral5m
	ur.ServiceTier = msg.Usage.ServiceTier
	ur.WebSearchCount = msg.Usage.ServerToolUse.WebSearch
	ur.WebFetchCount = msg.Usage.ServerToolUse.WebFetch
	ur.StopReason = msg.StopReason
	// Model-latency proxy: assistant turn ts − last user-role record ts.
	ur.DurationMs = clampedDeltaMs(s.lastUserTS, base.RecordTS)
	return ur
}

// contentBlock handles one content block: tool_use appends a tool Row and
// registers its id; tool_result correlates back to that Row by id, stamping both
// the call's duration and the size of what it returned onto the SAME row, so a
// tool's cost in time and its cost in context sit side by side.
func (s *claudeScan) contentBlock(base Row, msg *claudeMsg, b claudeContentBlock) {
	switch b.Type {
	case "tool_use":
		tr := base
		tr.Model = msg.Model
		tr.ToolName = b.Name
		tr.ToolUseID = b.ID
		tr.ToolInputHash, tr.ToolInputPreview = toolInputFingerprint(b.Input)
		tr.RunInBackground = runInBackground(b.Input)
		s.rows = append(s.rows, tr)
		if b.ID != "" {
			s.toolRowByID[b.ID] = len(s.rows) - 1
		}
	case "tool_result":
		if b.ToolUseID == "" {
			return
		}
		idx, ok := s.toolRowByID[b.ToolUseID]
		if !ok {
			return
		}
		if b.IsError {
			s.rows[idx].IsError = true
		}
		// tool_result.ts − tool_use.ts: the tool Row already carries its
		// tool_use timestamp in RecordTS.
		s.rows[idx].DurationMs = clampedDeltaMs(s.rows[idx].RecordTS, base.RecordTS)
		s.rows[idx].ToolResultBytes, s.rows[idx].ToolResultImages, s.rows[idx].ToolResultSpilled =
			measureToolResult(b.Content)
	}
}

// clampedDeltaMs returns (to − from) in whole milliseconds, clamped to >= 0. A
// zero endpoint (missing/garbage timestamp) or a negative delta (out-of-order
// records) yields 0 rather than a spurious huge or negative duration.
func clampedDeltaMs(from, to time.Time) int64 {
	if from.IsZero() || to.IsZero() {
		return 0
	}
	d := to.Sub(from).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}

// toolWhitespaceRe collapses any run of whitespace (incl. newlines) to a single
// space so a tool-input preview renders on one line. Hoisted to a package var so
// it compiles once, not per scanned tool_use block.
//
//nolint:gochecknoglobals // hoisted out of the parse hot loop per the hot-loop rule.
var toolWhitespaceRe = regexp.MustCompile(`\s+`)

// toolInputFingerprint returns a stable fnv64a hash of the canonicalized tool
// input (key-sorted, whitespace-independent — so semantically identical calls
// collide for duplicate-command detection) plus a short single-line preview. An
// empty input yields empty strings.
func toolInputFingerprint(input json.RawMessage) (hash, preview string) {
	if len(bytes.TrimSpace(input)) == 0 {
		return "", ""
	}
	h := fnv.New64a()
	_, _ = h.Write(canonicalJSON(input))
	return strconv.FormatUint(h.Sum64(), 16), toolInputPreview(input)
}

// canonicalJSON re-encodes a JSON payload into its canonical form (Go's encoder
// sorts map keys), so key order and whitespace do not perturb the hash. Payloads
// that do not parse as JSON hash on their raw bytes.
func canonicalJSON(input json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return input
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return input
	}
	return canon
}

// toolInputPreview renders a tool input as a short single line: the command
// string for a shell tool, else a compact rendering of the whole payload,
// truncated to 120 runes.
func toolInputPreview(input json.RawMessage) string {
	const maxRunes = 120
	var obj map[string]json.RawMessage
	if json.Unmarshal(input, &obj) == nil {
		if raw, ok := obj["command"]; ok {
			var cmd string
			if json.Unmarshal(raw, &cmd) == nil {
				return truncateRunes(collapseWhitespace(cmd), maxRunes)
			}
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, input) == nil {
		return truncateRunes(collapseWhitespace(buf.String()), maxRunes)
	}
	return truncateRunes(collapseWhitespace(string(input)), maxRunes)
}

// collapseWhitespace trims and single-spaces a string for one-line preview.
func collapseWhitespace(s string) string {
	return strings.TrimSpace(toolWhitespaceRe.ReplaceAllString(s, " "))
}

// truncateRunes returns the first n runes of s (rune-safe, never splitting a
// multi-byte character).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// parseClaudeFile opens a claude transcript and column-extracts it into Rows.
func parseClaudeFile(path string) ([]Row, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from Enumerate under ~/.claude/projects, not user text.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseClaude(f)
}
