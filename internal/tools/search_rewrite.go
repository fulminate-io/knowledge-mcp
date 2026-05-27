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

// widePoolSize is the wide-pool window for client-side rerank — matches
// cmd/knowledge/internal/rerank/voyage.go's voyageRerankerInputCap (1000)
// so the server's wide-fetch pool maps 1:1 to the Voyage rerank input.
const widePoolSize = 1000

// widePoolTopK is the top_k value sent in the Voyage rerank request body.
// Mirrors the pre-BCN6 OSS-side default (500). The reranker returns this
// many scored docs; the InterceptSearch trim further truncates to
// savedState.originalLimit before re-render.
const widePoolTopK = 500

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
