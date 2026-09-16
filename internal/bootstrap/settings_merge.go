// SPDX-License-Identifier: Apache-2.0

// settings_merge.go — the idempotent, NON-CLOBBERING JSON deep-merge that
// installs the knowledge-managed PreToolUse hook entries into the user's
// GLOBAL ~/.claude/settings.json. It is the JSON analog of managed_block.go's
// marker-based merger: where that splices a body between HTML-comment markers
// in a markdown file, this finds-or-replaces a SET of hook entries inside a
// structured JSON document.
//
// THE SET IS TWO ENTRIES, and that is why the sentinel is what it is. The
// promote-guard is a `command` hook carrying its marker in the command string;
// the session hook is an `http` hook, and an http hook object HAS NO command
// field at all — it carries `url` and `headers`. So an entry is classified as
// ours by the MARKER ALONE (knowledgeHookMarkerPrefix, found in a command or in
// any header value), never by its matcher. Classifying by matcher would (a)
// miss the http entry entirely, which would be written once and thereafter read
// as user content — never refreshed, invisible to the doctor drift check — and
// (b) strand a RETIRED managed entry in the file forever, because a matcher we
// no longer ship would read as something the user wrote.
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
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
)

const (
	// knowledgeHookMatcher is the PreToolUse matcher of the promote-guard
	// entry — the MCP tool name whose calls it inspects.
	knowledgeHookMatcher = "mcp__knowledge__collect"

	// knowledgeSessionHookMatcher is the PreToolUse matcher of the session
	// hook: every knowledge MCP tool, in Claude Code's mcp__<server>__<tool>
	// matcher grammar, because the session must be resolvable on ANY call.
	knowledgeSessionHookMatcher = "mcp__knowledge__.*"

	// knowledgeHookMarkerPrefix is the shared lead of every marker we author.
	// It is NOT the classifier — see knowledgeHookMarkers for why a prefix
	// match was too wide — but it keeps the marker values spelled from one
	// place.
	knowledgeHookMarkerPrefix = "knowledge-managed:"

	// knowledgePromoteHookMarker is the promote-guard's marker value, carried
	// as a leading shell no-op comment in its command.
	knowledgePromoteHookMarker = knowledgeHookMarkerPrefix + "promote-guard"

	// knowledgeSessionHookMarker is the session hook's marker value, carried
	// as the value of the knowledgeManagedHeader request header the hook sends
	// to the daemon. The daemon ignores the header; it exists so the entry
	// carries its own provenance where a reader of settings.json can see it.
	knowledgeSessionHookMarker = knowledgeHookMarkerPrefix + "session-hook"

	// knowledgeManagedHeader is the header name whose value carries the
	// session hook's marker.
	knowledgeManagedHeader = "X-Knowledge-Managed"
)

// knowledgeHookMarkers is THE sentinel: every marker value this installer has
// EVER shipped, matched exactly. It is the JSON analog of managed_block.go's
// managedBlockBegin/End markers (managed_block.go:26-27).
//
// EXACT VALUES, NOT THE PREFIX. A prefix match classified anything containing
// "knowledge-managed:" as ours — including a hook a USER wrote with their own
// marker under that lead. Combined with the splice's drop of a managed entry we
// no longer ship, that silently DELETED the user's hook from their own global
// settings.json. An exact list can only ever claim strings we authored.
//
// RETIRING A HOOK MEANS LEAVING ITS MARKER HERE, deliberately. A marker in this
// list but absent from the asset is how the splice recognizes an entry of ours
// to remove; drop the marker from the list instead and the retired entry is
// reclassified as user content and stranded in the file forever.
var knowledgeHookMarkers = []string{
	knowledgePromoteHookMarker,
	knowledgeSessionHookMarker,
}

// settingsHook models the subset of a single Claude Code hook-command object
// (.claude/settings.json hooks[]) needed to classify it: its type and
// command. It is used FOR INSPECTION ONLY — the merge reads Command and
// Headers to test for knowledgeHookMarkerPrefix and never re-marshals this
// struct back into the
// output. It deliberately does NOT model every field a hook object may carry
// (e.g. `timeout`, `statusMessage`); those survive because non-managed
// entries are passed through as raw bytes, not via this struct.
type settingsHook struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// managedMarker returns the knowledge-managed marker this hook object carries,
// and whether it carries one. The two places a hook object can hold a string we
// authored are the `command` of a command hook and a header VALUE of an http
// hook; the url is deliberately NOT a carrier, since it holds a user-chosen
// port and could not hold a stable literal.
//
// Only the EXACT values in knowledgeHookMarkers count. A hook carrying a marker
// of the user's own invention under our prefix is theirs.
func (h settingsHook) managedMarker() (string, bool) {
	for _, m := range knowledgeHookMarkers {
		if strings.Contains(h.Command, m) {
			return m, true
		}
		for _, v := range h.Headers {
			if strings.Contains(v, m) {
				return m, true
			}
		}
	}
	return "", false
}

// carriesManagedMarker reports whether this hook object carries one of our
// exact markers.
func (h settingsHook) carriesManagedMarker() bool {
	_, ok := h.managedMarker()
	return ok
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

	// (4)+(5) Build the managed entries from the canonical asset bytes, then
	// walk the existing entries, re-emitting every NON-managed entry as its
	// ORIGINAL bytes verbatim and replacing each managed one in place. The
	// decoded preToolUseEntry is used ONLY to classify — it is discarded,
	// never re-marshaled into the output.
	managed, err := canonicalManagedEntries(hookEntryJSON)
	if err != nil {
		return nil, err
	}
	out := spliceManagedEntries(preEntries, managed)

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

// managedEntry pairs a canonical knowledge-managed PreToolUse entry with its
// matcher, which is the key the splice uses to put a refreshed entry back in
// the slot its predecessor occupied.
type managedEntry struct {
	matcher string
	marker  string
	raw     json.RawMessage
}

// spliceManagedEntries walks the existing PreToolUse entries and returns the
// merged slice: every user entry re-emitted as its ORIGINAL bytes, every
// managed entry replaced IN PLACE by its canonical twin (first occurrence of a
// matcher wins the slot, further duplicates are dropped), and every canonical
// entry that had no predecessor appended in asset order.
//
// A managed entry whose MARKER we no longer ship is dropped, not preserved: it
// is ours by a marker only we have ever written, so leaving it behind would
// strand a retired hook in the user's settings forever with nothing left to
// refresh or remove it.
func spliceManagedEntries(existing []json.RawMessage, managed []managedEntry) []json.RawMessage {
	placed := make(map[string]bool, len(managed))
	out := make([]json.RawMessage, 0, len(existing)+len(managed))
	for _, raw := range existing {
		matcher, ours := managedEntryMatcher(raw, managed)
		if !ours {
			out = append(out, raw) // user entry, verbatim bytes
			continue
		}
		if placed[matcher] {
			continue // a duplicate of one already placed — drop it
		}
		for _, m := range managed {
			if m.matcher == matcher {
				out = append(out, m.raw)
				placed[matcher] = true
			}
		}
	}
	for _, m := range managed {
		if !placed[m.matcher] {
			out = append(out, m.raw) // none existed — append
		}
	}
	return out
}

// canonicalManagedEntries normalizes the embedded asset bytes into the exact
// json.RawMessages that will be spliced into hooks.PreToolUse, each paired with
// its matcher. It round-trips the KNOWLEDGE-OWNED asset (not user content)
// through json.Marshal so the managed entries are byte-stable across runs
// regardless of the asset file's own whitespace — the determinism the
// idempotency check relies on.
func canonicalManagedEntries(hookEntriesJSON []byte) ([]managedEntry, error) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(hookEntriesJSON, &entries); err != nil {
		return nil, fmt.Errorf("parse embedded hook asset: %w", err)
	}
	out := make([]managedEntry, 0, len(entries))
	for _, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("marshal managed hook entry: %w", err)
		}
		var head struct {
			Matcher string `json:"matcher"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, fmt.Errorf("read managed hook entry matcher: %w", err)
		}
		var decoded preToolUseEntry
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("read managed hook entry marker: %w", err)
		}
		marker := ""
		for _, hk := range decoded.Hooks {
			if m, ok := hk.managedMarker(); ok {
				marker = m
				break
			}
		}
		if marker == "" {
			return nil, fmt.Errorf("embedded hook entry %q carries no known knowledge-managed marker", head.Matcher)
		}
		out = append(out, managedEntry{matcher: head.Matcher, marker: marker, raw: raw})
	}
	return out, nil
}

// managedEntryMatcher classifies a raw PreToolUse entry and returns its matcher
// when the entry is knowledge-managed. It decodes into preToolUseEntry FOR
// INSPECTION ONLY; the struct is discarded. A raw entry that does not parse as
// a hook entry (malformed user content we should not touch) is non-managed.
//
// CLASSIFICATION IS THE MARKER **AND**, FOR A MARKER WE STILL SHIP, ITS OWN
// MATCHER. That second half is what keeps a user's copy of our hook theirs: a
// user who lifted the promote-guard and re-pointed it at another tool has our
// exact marker on a matcher we do not ship, and the marker alone would classify
// that as ours and the splice would DELETE it from their global settings. A
// marker we have RETIRED — in knowledgeHookMarkers, absent from the asset —
// carries no such ambiguity: only we ever wrote it and there is no current
// entry it could be a copy of, so it is ours on any matcher and is removed.
func managedEntryMatcher(raw json.RawMessage, managed []managedEntry) (string, bool) {
	var entry preToolUseEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", false
	}
	for _, h := range entry.Hooks {
		marker, ok := h.managedMarker()
		if !ok {
			continue
		}
		shipped, shippedMatcher := shippedMarkerMatcher(marker, managed)
		if shipped && shippedMatcher != entry.Matcher {
			continue // our marker, someone else's matcher: a user's copy
		}
		return entry.Matcher, true
	}
	return "", false
}

// shippedMarkerMatcher reports the matcher the asset currently ships for
// marker, and whether the asset ships it at all. A marker the asset no longer
// carries is retired.
func shippedMarkerMatcher(marker string, managed []managedEntry) (bool, string) {
	for _, m := range managed {
		if m.marker == marker {
			return true, m.matcher
		}
	}
	return false, ""
}

// isManagedEntry reports whether a raw PreToolUse entry is knowledge-managed,
// classified against the entries the CURRENT asset ships.
func isManagedEntry(raw json.RawMessage) bool {
	managed, err := canonicalManagedEntries(assets.ClaudeHooks)
	if err != nil {
		return false
	}
	_, ok := managedEntryMatcher(raw, managed)
	return ok
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
