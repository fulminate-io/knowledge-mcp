// SPDX-License-Identifier: Apache-2.0

// settings_merge.go — the idempotent, NON-CLOBBERING JSON deep-merge that
// installs the knowledge-managed promote-guard PreToolUse hook into the
// user's GLOBAL ~/.claude/settings.json. It is the JSON analog of
// managed_block.go's marker-based merger: where that splices a body between
// HTML-comment markers in a markdown file, this finds-or-replaces a single
// hook entry inside a structured JSON document.
//
// The merge writes the user's own global config, so losslessness is the
// hard requirement: every top-level key, every other hook event, and every
// PreToolUse entry the user authored survives. Losslessness is achieved by
// json.RawMessage passthrough at every level — the typed structs below are
// used ONLY to inspect (classify managed-vs-user entries); user content is
// NEVER round-tripped through them (encoding/json silently drops fields a
// struct does not model, e.g. a hook's `timeout` or `statusMessage`).

package bootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// knowledgeHookMatcher is the PreToolUse matcher value of the single
	// knowledge-managed hook entry — the MCP tool name whose calls the
	// promote-guard inspects. Together with knowledgeHookMarker it forms
	// the structured sentinel that lets the merge find+replace the managed
	// entry idempotently — the JSON analog of managed_block.go's
	// managedBlockBegin/managedBlockEnd markers (managed_block.go:26-27).
	knowledgeHookMatcher = "mcp__knowledge__collect"

	// knowledgeHookMarker is a stable substring embedded in the managed
	// hook's command (a leading shell no-op comment in claude_hooks.json).
	// An entry is classified as knowledge-managed iff its matcher equals
	// knowledgeHookMatcher AND one of its hook commands contains this
	// marker — see mergeClaudeSettings.
	knowledgeHookMarker = "knowledge-managed:promote-guard"
)

// settingsHook models the subset of a single Claude Code hook-command object
// (.claude/settings.json hooks[]) needed to classify it: its type and
// command. It is used FOR INSPECTION ONLY — the merge reads Command to test
// for knowledgeHookMarker and never re-marshals this struct back into the
// output. It deliberately does NOT model every field a hook object may carry
// (e.g. `timeout`, `statusMessage`); those survive because non-managed
// entries are passed through as raw bytes, not via this struct.
type settingsHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// preToolUseEntry models the subset of a single PreToolUse matcher entry
// (.claude/settings.json hooks.PreToolUse[]) needed to classify it: its
// matcher and its list of hook commands. Like settingsHook it is used FOR
// INSPECTION ONLY — the merge reads Matcher and Hooks[].Command to decide
// whether an entry is knowledge-managed, then discards the decoded struct
// and re-emits the original raw bytes for every non-managed entry. It is a
// PARTIAL model by design; losslessness comes from the json.RawMessage
// passthrough in mergeClaudeSettings, not from this struct being total.
type preToolUseEntry struct {
	Matcher string         `json:"matcher"`
	Hooks   []settingsHook `json:"hooks"`
}

// mergeClaudeSettings returns the settings.json content with the single
// knowledge-managed promote-guard hook entry inserted or refreshed under
// hooks.PreToolUse. It is a PURE function (no I/O) — the JSON analog of
// mergeManagedBlock (managed_block.go:49) — so it is trivially testable.
// hookEntryJSON is the canonical managed entry (assets.ClaudeHooks).
//
// Losslessness is the contract: every user top-level key, every other hook
// event, and every non-managed PreToolUse entry is preserved intact
// (re-indented to the file's 2-space style by the final MarshalIndent) and
// value-equal across re-runs; only the single knowledge-managed PreToolUse
// entry is (re)written. The mechanism is json.RawMessage passthrough at
// every level: the top-level map, the hooks map, and the PreToolUse slice
// all carry user content as raw bytes, so fields the inspection structs do
// not model (e.g. a hook's `timeout` or `statusMessage`) ride through
// untouched — the raw entry bytes are spliced in before the document-wide
// MarshalIndent merely normalizes whitespace. NOTE: this is value-equal,
// NOT file-level "byte-for-byte" — MarshalIndent re-indents RawMessage
// values to the output's 2-space style.
//
// Idempotency: encoding/json marshals map[string]T with keys sorted
// lexicographically, so repeated marshals of the same logical content are
// byte-identical — that is what makes the in-sync/idempotency checks hold
// from run 2 onward despite Go maps being unordered. Run 1 against a
// pre-existing hand-formatted file legitimately differs (one-time re-indent
// + entry insertion); that normalization is expected, not a regression.
func mergeClaudeSettings(existing []byte, hookEntryJSON []byte) ([]byte, error) {
	// (1) Top level → map[string]json.RawMessage, preserving every key
	// (model, env, permissions, ...) as raw bytes. Empty/whitespace input
	// starts from an empty object (the {}-if-absent case).
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &top); err != nil {
			return nil, fmt.Errorf("parse settings.json: %w", err)
		}
	}

	// (2) hooks value → map[string]json.RawMessage, preserving other hook
	// EVENTS (SessionStart, PostToolUse, ...) as raw bytes.
	hooks := map[string]json.RawMessage{}
	if raw, ok := top["hooks"]; ok && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, fmt.Errorf("parse settings.json hooks: %w", err)
		}
	}

	// (3) PreToolUse value → []json.RawMessage (RAW entries, not a typed
	// slice), preserving every user entry as raw bytes.
	var preEntries []json.RawMessage
	if raw, ok := hooks["PreToolUse"]; ok && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &preEntries); err != nil {
			return nil, fmt.Errorf("parse settings.json hooks.PreToolUse: %w", err)
		}
	}

	// (4)+(5) Build the managed entry from the canonical asset bytes, then
	// walk the existing entries: re-emit every NON-managed entry as its
	// ORIGINAL bytes verbatim, and REPLACE a pre-existing managed entry in
	// place (preserve position). The decoded preToolUseEntry is used ONLY
	// to classify — it is discarded, never re-marshaled into the output.
	managedEntry, err := canonicalManagedEntry(hookEntryJSON)
	if err != nil {
		return nil, err
	}

	out := make([]json.RawMessage, 0, len(preEntries)+1)
	replaced := false
	for _, raw := range preEntries {
		if isManagedEntry(raw) {
			// Replace in place (first managed entry wins its slot); drop
			// any further managed duplicates so the result has exactly one.
			if !replaced {
				out = append(out, managedEntry)
				replaced = true
			}
			continue
		}
		out = append(out, raw) // user entry, verbatim bytes
	}
	if !replaced {
		out = append(out, managedEntry) // none existed — append
	}

	// (6) Re-marshal bottom-up: PreToolUse slice → hooks map → top map,
	// then MarshalIndent the whole document at the file's 2-space style.
	preRaw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal hooks.PreToolUse: %w", err)
	}
	hooks["PreToolUse"] = preRaw

	hooksRaw, err := json.Marshal(hooks)
	if err != nil {
		return nil, fmt.Errorf("marshal hooks: %w", err)
	}
	top["hooks"] = hooksRaw

	pretty, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings.json: %w", err)
	}
	return append(pretty, '\n'), nil
}

// canonicalManagedEntry normalizes the embedded asset bytes into the exact
// json.RawMessage that will be spliced into hooks.PreToolUse. It round-trips
// the KNOWLEDGE-OWNED asset (not user content) through json.Marshal so the
// managed entry is byte-stable across runs regardless of the asset file's
// own whitespace — the determinism the idempotency check relies on.
func canonicalManagedEntry(hookEntryJSON []byte) (json.RawMessage, error) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(hookEntryJSON, &entry); err != nil {
		return nil, fmt.Errorf("parse embedded hook asset: %w", err)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("marshal managed hook entry: %w", err)
	}
	return raw, nil
}

// isManagedEntry classifies a raw PreToolUse entry as the knowledge-managed
// one: matcher==knowledgeHookMatcher AND some hooks[].command contains
// knowledgeHookMarker. It decodes into preToolUseEntry FOR INSPECTION ONLY;
// the struct is discarded. A raw entry that does not parse as a hook entry
// (malformed user content we should not touch) is treated as non-managed.
func isManagedEntry(raw json.RawMessage) bool {
	var entry preToolUseEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return false
	}
	if entry.Matcher != knowledgeHookMatcher {
		return false
	}
	for _, h := range entry.Hooks {
		if bytes.Contains([]byte(h.Command), []byte(knowledgeHookMarker)) {
			return true
		}
	}
	return false
}

// writeClaudeSettings merges the knowledge-managed promote-guard hook
// (hookEntryJSON, normally assets.ClaudeHooks) into the settings.json at
// path, clobber-safe. Mirrors writeManagedFile (managed_block.go:88):
//   - No existing file → create it containing the user's (empty) settings
//     plus the managed entry.
//   - Existing file → merge in place via mergeClaudeSettings, preserving
//     every user top-level key, hook event, and non-managed PreToolUse
//     entry value-equal.
//
// Returns whether the file content changed (false when already in sync), so
// the caller can report accurately in dry-run mode. A missing file is not an
// error (treated as empty — the {}-if-absent case); a real read error
// propagates.
func writeClaudeSettings(path string, hookEntryJSON []byte, dryRun bool) (changed bool, err error) {
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read %s: %w", path, readErr)
	}

	next, err := mergeClaudeSettings(existing, hookEntryJSON)
	if err != nil {
		return false, err
	}
	if bytes.Equal(next, existing) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, next, 0o644); err != nil { //nolint:gosec // user-readable config, 0644 is correct
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// settingsInSync reports whether the settings.json at path already carries
// the knowledge-managed hook entry in its current form (an in-sync file is
// one where re-merging hookEntryJSON changes nothing). Mirrors
// managedBlockInSync (managed_block.go:117): exists is false when the file
// is absent (a not-found read is not an error here — callers treat a missing
// file as "needs install"). It is the cheap drift signal shared by the
// doctor check; it never false-positives on user content because
// mergeClaudeSettings only (re)writes the single managed entry.
func settingsInSync(path string, hookEntryJSON []byte) (inSync, exists bool, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	next, err := mergeClaudeSettings(data, hookEntryJSON)
	if err != nil {
		return false, true, err
	}
	return bytes.Equal(next, data), true, nil
}
