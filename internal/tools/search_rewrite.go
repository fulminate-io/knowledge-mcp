// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/rerank"
)

// widePoolSize is the OPERATING candidate count for client-side rerank —
// how many documents the server's wide fetch gathers and the reranker
// scores. It sits deliberately far below the Voyage API's hard document
// ceiling (voyageRerankerInputCap in cmd/knowledge/internal/rerank/voyage.go):
// that ceiling states what the API will accept, this constant states what
// we choose to send. Rerank cost is billed and rate-limited per token and
// latency rises with payload, so the pool is sized to the work actually
// needed rather than to the maximum permitted.
const widePoolSize = 100

// widePoolTopK is the top_k value sent in the Voyage rerank request body.
// Equal to the pool: above the document count top_k is dead weight in the
// request body, and below it the reranker would discard docs it already
// scored. The InterceptSearch trim further truncates to
// savedState.originalLimit before re-render.
const widePoolTopK = 100

// rerankCallerLimitCeiling is the declared maximum for the caller-facing
// `limit`, mirroring the tool schema. Enforced so contract and behavior agree.
const rerankCallerLimitCeiling = 50

// clampCallerLimit bounds a caller-supplied limit to the declared maximum,
// reporting whether the clamp engaged so the caller can be told. It serves the
// whole search-tool boundary and the recall arm — not one retrieval strategy —
// which is why its name is not rerank-specific.
//
// A non-positive requested value passes through UNTOUCHED rather than being
// substituted with the ceiling. Callers invoke this unconditionally, so it sees
// absent limits; substituting 50 there would silently WIDEN every default
// request (search defaults to 10, recall to 20) instead of narrowing an
// over-large one. Only the over-ceiling case clamps.
func clampCallerLimit(requested int) (limit int, clamped bool) {
	if requested > rerankCallerLimitCeiling {
		return rerankCallerLimitCeiling, true
	}
	return requested, false
}

// searchLimitClampNotice is the caller-facing disclosure that the declared
// `limit` maximum engaged, emitted by the search-tool boundary. Its twin on the
// recall arm (recallLimitClampNotice, intercept_thoughts_recall.go) carries the
// same copy: the text is duplicated rather than shared so each arm's disclosure
// is greppable at its own site.
const searchLimitClampNotice = "Showing 50 results — the declared `limit` maximum of 50 engaged, so this result may be incomplete."

// clampSearchCallerLimit bounds the `limit` inside a raw search payload to the
// declared maximum, returning the (possibly rewritten) args and whether the
// clamp engaged. Absent, non-positive and malformed payloads pass through
// unchanged with clamped=false — the same fail-open pass-through rewriteSearchArgs
// documents, so a payload this cannot parse still reaches the handler that can
// produce a good error for it.
//
// THE ORDER MATTERS AND IT IS NOT NEGOTIABLE. On a keyed, non-BM25-only search
// the `limit` key is written by two different concerns meaning two different
// things: this clamp writes what the CALLER may receive, and the rerank rewrite
// later overwrites it with how many candidates the RERANKER should score.
// Between them captureSavedState reads the clamped value into originalLimit,
// which is what the post-rerank trim cuts to. So the sequence must be
// clamp, then capture, then widen.
//
// Clamping after the widening — or folding the two into one "effective limit" —
// writes 50 over the candidate pool and silently guts the rerank input, while
// leaving every pure-function unit test green. That is why the clamp lives in
// the thin outer, strictly before the arms run.
func clampSearchCallerLimit(rawArgs []byte) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawArgs, &obj); err != nil {
		return rawArgs, false
	}
	raw, ok := obj["limit"]
	if !ok {
		return rawArgs, false
	}
	var lim int
	if err := json.Unmarshal(raw, &lim); err != nil || lim <= 0 {
		return rawArgs, false
	}
	clampedLim, clamped := clampCallerLimit(lim)
	if !clamped {
		return rawArgs, false
	}
	enc, err := json.Marshal(clampedLim)
	if err != nil {
		return rawArgs, false
	}
	obj["limit"] = enc
	out, err := json.Marshal(obj)
	if err != nil {
		return rawArgs, false
	}
	return out, true
}

// savedState is the slice of original args the post-rerank pipeline needs
// to re-render the response for the caller. Populated by rewriteSearchArgs
// whenever the rewrite fires; consumed by applyClientRerank.
type savedState struct {
	originalLimit    int      // for trim-to-N post-rerank; defaults to 10
	originalFormat   string   // "text" or "json" — re-render format
	originalRerank   *bool    // preserve the tri-state (informational)
	originalQuery    string   // joined query label for the re-render header
	originalFields   []string // projection — replayed client-side post-rerank
	clientSideActive bool     // true when hasReranker drove the rewrite
}

// rewriteSearchArgs is the pure-function rewrite stage of InterceptSearch.
// Two concerns:
//
//  1. strategies_file → strategies expansion: read the file path, validate
//     via rerank.ParsePipeline, substitute the raw file bytes under the
//     `strategies` wire key, drop strategies_file.
//  2. Client-side rerank rewrite (when hasReranker): widen `limit` to
//     widePoolSize, coerce `format` to "json", drop the `fields` projection
//     (re-applied client-side post-rerank). Does NOT inject a
//     client_side_rerank wire field — there is none; the args rewrite itself IS
//     the signal.
//
// Return contract:
//   - (rawArgs, _, false, nil)        — neither rewrite applies; passthrough.
//   - (rewritten, saved, true, nil)   — at least one rewrite applied.
//   - (nil, _, true, err)             — file read or parse error.
//   - (rawArgs, _, false, nil)        — rawArgs is malformed JSON; let the
//     server's normal handler surface that error.
//
// Pure function: bytes-in, (bytes-out, savedState, hasRewrite, err). The
// only I/O is os.ReadFile on strategies_file (when set). Tested in
// isolation via search_test.go.
func rewriteSearchArgs(rawArgs []byte, hasReranker bool) ([]byte, savedState, bool, error) {
	var argMap map[string]json.RawMessage
	if err := json.Unmarshal(rawArgs, &argMap); err != nil {
		// Intentional pass-through: malformed JSON yields a better error
		// from the server's normal handler than we can fabricate here.
		return rawArgs, savedState{}, false, nil //nolint:nilerr
	}

	saved := captureSavedState(argMap, hasReranker)

	stratHit, err := applyStrategiesExpansion(argMap)
	if err != nil {
		return nil, savedState{}, true, err
	}

	rerankHit := applyClientSideRerankRewrite(argMap, hasReranker)
	slog.Debug("rerank-trace: rewriteSearchArgs",
		"has_reranker", hasReranker, "strategies_hit", stratHit, "rerank_hit", rerankHit)

	if !stratHit && !rerankHit {
		return rawArgs, savedState{}, false, nil
	}

	out, err := json.Marshal(argMap)
	if err != nil {
		return nil, savedState{}, true, fmt.Errorf("args remarshal: %w", err)
	}
	return out, saved, true, nil
}

// captureSavedState snapshots the original args fields the post-rerank
// pipeline needs to re-render the response. Idempotent — call before any
// rewrite mutates argMap. Each field decode is best-effort: malformed
// values fall back to defaults rather than failing the whole rewrite.
func captureSavedState(argMap map[string]json.RawMessage, hasReranker bool) savedState {
	saved := savedState{originalLimit: 10, originalFormat: "text", clientSideActive: hasReranker}
	if raw, ok := argMap["limit"]; ok {
		var lim int
		// Recorded VERBATIM: the args arrive already clamped by the search-tool
		// boundary, so re-clamping here would be a second authority over the same
		// value. The lim > 0 guard stays — an explicit "limit": 0 must leave the
		// struct default standing, because applyClientRerank's trim is gated on
		// originalLimit > 0 and a zero would skip the trim entirely.
		if err := json.Unmarshal(raw, &lim); err == nil && lim > 0 {
			saved.originalLimit = lim
		}
	}
	if raw, ok := argMap["format"]; ok {
		var f string
		if err := json.Unmarshal(raw, &f); err == nil && f != "" {
			saved.originalFormat = f
		}
	}
	if raw, ok := argMap["rerank"]; ok {
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			saved.originalRerank = &b
		}
	}
	if raw, ok := argMap["fields"]; ok {
		var fields []string
		if err := json.Unmarshal(raw, &fields); err == nil {
			saved.originalFields = fields
		}
	}
	saved.originalQuery = extractQueryLabel(argMap)
	return saved
}

// extractQueryLabel joins "query" + "queries[]" into a single human-readable
// header for the re-render. Used by renderForCaller as the response label.
func extractQueryLabel(argMap map[string]json.RawMessage) string {
	var parts []string
	if raw, ok := argMap["query"]; ok {
		var q string
		if err := json.Unmarshal(raw, &q); err == nil && q != "" {
			parts = append(parts, q)
		}
	}
	if raw, ok := argMap["queries"]; ok {
		var qs []string
		if err := json.Unmarshal(raw, &qs); err == nil {
			parts = append(parts, qs...)
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts, " | ")
	}
}

// applyStrategiesExpansion reads the optional `strategies_file` key, validates
// the file via rerank.ParsePipeline, and substitutes the raw file bytes under
// the `strategies` wire key. No-op when strategies_file is absent or empty.
// Returns (true, nil) when the expansion fired; (false, nil) when no-op;
// (false, err) on read/parse failure.
func applyStrategiesExpansion(argMap map[string]json.RawMessage) (bool, error) {
	raw, ok := argMap["strategies_file"]
	if !ok {
		return false, nil
	}
	var path string
	if err := json.Unmarshal(raw, &path); err != nil {
		return false, fmt.Errorf("strategies_file decode: %w", err)
	}
	if path == "" {
		return false, nil
	}
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read strategies_file %q: %w", path, err)
	}
	if _, err := rerank.ParsePipeline(fileBytes); err != nil {
		return false, fmt.Errorf("strategies_file %q: %w", path, err)
	}
	delete(argMap, "strategies_file")
	argMap["strategies"] = json.RawMessage(fileBytes)
	return true, nil
}

// applyClientSideRerankRewrite widens `limit` to widePoolSize and coerces
// `format` to "json" so the wide pool reaches the client unmodified and
// the client hydrator can parse the structured response. Strips `fields`
// (re-applied client-side post-rerank). No-op when hasReranker is false.
// Returns true when the rewrite fired (always true when hasReranker is
// true — the rewrite is unconditional for the client-side path). The
// literal byte slices are constructed inline so the json.Marshal error
// path (which can't fire on these statically-typed inputs) doesn't have
// to bubble up through the caller chain.
func applyClientSideRerankRewrite(argMap map[string]json.RawMessage, hasReranker bool) bool {
	if !hasReranker {
		return false
	}
	argMap["limit"] = json.RawMessage(fmt.Sprintf("%d", widePoolSize))
	argMap["format"] = json.RawMessage(`"json"`)
	delete(argMap, "fields")
	return true
}
