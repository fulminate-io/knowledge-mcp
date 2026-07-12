// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
)

// Codex rollouts are LAYERED records: a line is {timestamp,type,payload} and the
// per-record state needed to attribute a token row (session id, cwd, cli_version,
// git branch, current model) lives in EARLIER lines (the line-1 session_meta and
// the per-turn turn_context). The parser therefore carries scan state across
// lines — that is why these structs are richer than hivemonitor's liveness-only
// codexRolloutPayload (which models no token/model/meta fields).

// codexErrorRe is the best-effort Codex tool-failure heuristic: Codex rollouts
// carry NO structured per-call error flag, so a non-zero "Process exited with
// code N" in a function_call_output is the strongest signal available. Hoisted
// to a package var so it is compiled once, not per scanned line.
//
//nolint:gochecknoglobals // hoisted out of the parse hot loop per the hot-loop rule.
var codexErrorRe = regexp.MustCompile(`Process exited with code [1-9]`)

// codexEnvelope is one rollout line: a timestamp, a top-level type discriminator,
// and a payload decoded per-type.
type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// codexSessionMetaPayload is the line-1 session_meta payload. Its fields seed the
// scan state carried onto every subsequent Row.
type codexSessionMetaPayload struct {
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	CLIVersion string `json:"cli_version"`
	Git        struct {
		Branch     string `json:"branch"`
		CommitHash string `json:"commit_hash"`
	} `json:"git"`
}

// codexTurnContextPayload carries the model in effect for the turn; the parser
// updates its current-model state on each one.
type codexTurnContextPayload struct {
	Model string `json:"model"`
}

// codexTokenUsage is one usage block. Codex reports output_tokens and
// reasoning_output_tokens SEPARATELY (reasoning is NOT already in output); the
// parser folds reasoning into the output column since reasoning bills at the
// output rate and the 16-column contract has no reasoning column.
type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// codexEventMsgPayload is an event_msg payload; only the token_count variant is
// extracted. last_token_usage is the per-turn delta — total_token_usage is the
// running cumulative and is deliberately NOT used (summing totals double-counts).
type codexEventMsgPayload struct {
	Type string `json:"type"`
	Info struct {
		LastTokenUsage  *codexTokenUsage `json:"last_token_usage"`
		TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
	} `json:"info"`
}

// codexResponseItemPayload is a response_item payload: a function_call carries the
// tool name + call_id; a function_call_output carries the matching call_id and the
// tool's output text (scanned for the error heuristic).
type codexResponseItemPayload struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

// codexScanState carries the per-file state a codex rollout needs to attribute a
// Row: the session_meta-seeded identity (session/cwd/cli_version/git branch), the
// current turn model, the accumulated Rows, and the call_id→tool-Row index used
// to flip IsError from a later function_call_output.
type codexScanState struct {
	rows            []Row
	toolRowByCallID map[string]int

	sessionID  string
	project    string
	cliVersion string
	gitBranch  string
	model      string
}

// baseRow builds a Row pre-filled with the carried identity + current model. The
// caller fills in the per-record token/tool columns.
func (st *codexScanState) baseRow(ts string, recordStart int64, recordType string) Row {
	return Row{
		Source:       SourceCodex,
		SessionID:    st.sessionID,
		Project:      st.project,
		GitBranch:    st.gitBranch,
		RecordTS:     parseRecordTS(ts),
		RecordType:   recordType,
		Model:        st.model,
		CLIVersion:   st.cliVersion,
		SourceOffset: recordStart,
	}
}

// applySessionMeta seeds the carried identity from the line-1 session_meta.
func (st *codexScanState) applySessionMeta(payload json.RawMessage) {
	var p codexSessionMetaPayload
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	st.sessionID = p.ID
	st.project = p.Cwd
	st.cliVersion = p.CLIVersion
	st.gitBranch = p.Git.Branch
}

// applyTurnContext updates the current model carried onto subsequent token Rows.
func (st *codexScanState) applyTurnContext(payload json.RawMessage) {
	var p codexTurnContextPayload
	if json.Unmarshal(payload, &p) == nil && p.Model != "" {
		st.model = p.Model
	}
}

// applyEventMsg emits a token Row from a token_count's last_token_usage (NOT
// total — summing totals double-counts), folding reasoning into output.
func (st *codexScanState) applyEventMsg(payload json.RawMessage, ts string, recordStart int64) {
	var p codexEventMsgPayload
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	if p.Type != "token_count" || p.Info.LastTokenUsage == nil {
		return
	}
	u := p.Info.LastTokenUsage
	row := st.baseRow(ts, recordStart, p.Type)
	row.InputTokens = u.InputTokens
	row.OutputTokens = u.OutputTokens + u.ReasoningOutputTokens // FOLD reasoning into output
	row.CacheReadTokens = u.CachedInputTokens                   // cached_input is its OWN column; not folded into input
	row.CacheCreationTokens = 0                                 // Codex has no cache_creation equivalent
	st.rows = append(st.rows, row)
}

// applyResponseItem emits a tool Row for a function_call and flips that Row's
// IsError (best-effort) when the matching function_call_output trips the error
// heuristic.
func (st *codexScanState) applyResponseItem(payload json.RawMessage, ts string, recordStart int64) {
	var p codexResponseItemPayload
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	switch p.Type {
	case "function_call":
		row := st.baseRow(ts, recordStart, p.Type)
		row.ToolName = p.Name
		st.rows = append(st.rows, row)
		if p.CallID != "" {
			st.toolRowByCallID[p.CallID] = len(st.rows) - 1
		}
	case "function_call_output":
		// Best-effort error attribution (no structured flag exists — a non-zero
		// process exit in the output text marks the call errored) plus duration:
		// output.ts − the paired function_call.ts (carried in the tool Row's
		// RecordTS). The other enrichment columns have no Codex analog and stay at
		// their zero/empty values, preserving schema parity with claude rows.
		if p.CallID != "" {
			if idx, ok := st.toolRowByCallID[p.CallID]; ok {
				if codexErrorRe.Match(p.Output) {
					st.rows[idx].IsError = true
				}
				st.rows[idx].DurationMs = clampedDeltaMs(st.rows[idx].RecordTS, parseRecordTS(ts))
			}
		}
	}
}

// ParseCodex column-extracts a codex rollout (layered records requiring carried
// scan state) into Rows. session_meta (line 1) seeds session id/cwd/cli_version/
// git branch; each turn_context updates the current model; each token_count emits
// a token Row from last_token_usage (NOT total); each function_call emits a tool
// Row whose IsError is set best-effort when the matching function_call_output's
// text trips the error heuristic and whose duration_ms is the paired
// output.ts−call.ts. Codex records carry no per-record uuid, so UUID/ParentUUID
// stay empty; Codex also has no subagent/attribution/cache-split/service-tier
// concepts, so those enrichment columns stay at their zero/empty values (schema
// parity with claude rows is preserved by the shared Row shape). The scan is
// tolerant: blank/non-JSON lines and a half-written trailing line are skipped
// rather than erroring.
func ParseCodex(r io.Reader) ([]Row, error) {
	st := &codexScanState{toolRowByCallID: map[string]int{}}

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

		if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
			continue
		}
		var env codexEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			// Malformed or half-written trailing line — skip, keep parsing.
			continue
		}

		switch env.Type {
		case "session_meta":
			st.applySessionMeta(env.Payload)
		case "turn_context":
			st.applyTurnContext(env.Payload)
		case "event_msg":
			st.applyEventMsg(env.Payload, env.Timestamp, recordStart)
		case "response_item":
			st.applyResponseItem(env.Payload, env.Timestamp, recordStart)
		}
	}
	if err := sc.Err(); err != nil {
		return st.rows, err
	}
	return st.rows, nil
}

// parseCodexFile opens a codex rollout and column-extracts it into Rows.
func parseCodexFile(path string) ([]Row, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from Enumerate under ~/.codex/sessions, not user text.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseCodex(f)
}
