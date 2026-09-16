// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// hookAsset is the canonical managed entry SET the merge installs — the
// promote-guard command hook and the http session hook — rendered at the
// default daemon port. The asset carries a port placeholder, so an unrendered
// asset is never what the installer writes and never what a test should merge.
func hookAsset() []byte {
	rendered, err := renderClaudeHooks(assets.ClaudeHooks, graphclient.DefaultMCPHTTPPort)
	if err != nil {
		panic("render the embedded claude hook asset: " + err.Error())
	}
	return rendered
}

// wantManagedEntries is how many knowledge-managed PreToolUse entries the asset
// ships, READ OFF THE ASSET rather than spelled: a test pinning the count must
// not have to be edited when the set changes, only when the MERGE stops
// installing what the asset holds.
func wantManagedEntries() int {
	entries, err := canonicalManagedEntries(hookAsset())
	if err != nil {
		panic("decode the embedded claude hook asset: " + err.Error())
	}
	return len(entries)
}

// promoteGuardEntry returns the promote-guard entry out of the asset set, for
// the tests that exercise its command.
func promoteGuardEntry(t *testing.T) []byte {
	t.Helper()
	for _, e := range mustCanonicalEntries(t) {
		if e.matcher == knowledgeHookMatcher {
			return e.raw
		}
	}
	t.Fatalf("no promote-guard entry (matcher %q) in the hook asset", knowledgeHookMatcher)
	return nil
}

func mustCanonicalEntries(t *testing.T) []managedEntry {
	t.Helper()
	entries, err := canonicalManagedEntries(hookAsset())
	if err != nil {
		t.Fatalf("canonicalManagedEntries: %v", err)
	}
	return entries
}

// decodePreToolUse decodes a settings.json document into its hooks.PreToolUse
// slice of RAW entries, for assertions that inspect individual entries.
func decodePreToolUse(t *testing.T, doc []byte) []json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		t.Fatalf("decode top level: %v\n%s", err, doc)
	}
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(top["hooks"], &hooks); err != nil {
		t.Fatalf("decode hooks: %v", err)
	}
	var pre []json.RawMessage
	if err := json.Unmarshal(hooks["PreToolUse"], &pre); err != nil {
		t.Fatalf("decode PreToolUse: %v", err)
	}
	return pre
}

// countManagedEntries returns how many PreToolUse entries are classified as
// the knowledge-managed promote-guard.
func countManagedEntries(t *testing.T, doc []byte) int {
	t.Helper()
	n := 0
	for _, e := range decodePreToolUse(t, doc) {
		if isManagedEntry(e) {
			n++
		}
	}
	return n
}

// TestMergeClaudeSettings_Fresh: empty input → a JSON object whose
// hooks.PreToolUse holds exactly the managed entry.
func TestMergeClaudeSettings_Fresh(t *testing.T) {
	out, err := mergeClaudeSettings(nil, hookAsset())
	if err != nil {
		t.Fatalf("mergeClaudeSettings(empty): %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("output is not valid JSON:\n%s", out)
	}
	pre := decodePreToolUse(t, out)
	if len(pre) != wantManagedEntries() {
		t.Fatalf("fresh PreToolUse has %d entries, want %d:\n%s", len(pre), wantManagedEntries(), out)
	}
	for i, e := range pre {
		if !isManagedEntry(e) {
			t.Errorf("fresh entry %d is not a managed entry:\n%s", i, e)
		}
	}
}

// TestMergeClaudeSettings_NonClobber locks the LOSSLESS model. It seeds a
// settings.json with user top-level keys (model/env/permissions), a user
// PreToolUse "Bash" entry whose hook-command carries fields the inspection
// structs deliberately do NOT model — a `timeout` (number) AND a
// `statusMessage` (string), exactly the unmodeled shape live at
// .claude/settings.json:34 — and a SessionStart event. After merge it
// asserts every user key/event survives AND the Bash entry survives
// value-equal INCLUDING timeout + statusMessage. This FAILS if PreToolUse
// were modeled as a typed []preToolUseEntry (the lossy struct drops
// timeout/statusMessage on re-marshal) and PASSES under the
// []json.RawMessage passthrough.
func TestMergeClaudeSettings_NonClobber(t *testing.T) {
	seed := []byte(`{
  "model": "opus",
  "env": {"KNOWLEDGE_FOO": "bar"},
  "permissions": {"allow": ["Edit(x)"], "deny": ["Write(y)"]},
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "echo guard", "timeout": 60, "statusMessage": "running bash guard"}
        ]
      }
    ],
    "SessionStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "echo start"}]}
    ]
  }
}`)

	out, err := mergeClaudeSettings(seed, hookAsset())
	if err != nil {
		t.Fatalf("mergeClaudeSettings(seed): %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("output not valid JSON:\n%s", out)
	}

	// Top-level user keys survive value-equal.
	var seedTop, outTop map[string]json.RawMessage
	if err := json.Unmarshal(seed, &seedTop); err != nil {
		t.Fatalf("decode seed top: %v", err)
	}
	if err := json.Unmarshal(out, &outTop); err != nil {
		t.Fatalf("decode out top: %v", err)
	}
	for _, k := range []string{"model", "env", "permissions"} {
		if !jsonValueEqual(t, seedTop[k], outTop[k]) {
			t.Errorf("top-level key %q not preserved value-equal:\nwant %s\ngot  %s", k, seedTop[k], outTop[k])
		}
	}

	// The SessionStart event survives value-equal.
	var seedHooks, outHooks map[string]json.RawMessage
	json.Unmarshal(seedTop["hooks"], &seedHooks)
	json.Unmarshal(outTop["hooks"], &outHooks)
	if !jsonValueEqual(t, seedHooks["SessionStart"], outHooks["SessionStart"]) {
		t.Errorf("SessionStart event not preserved value-equal:\nwant %s\ngot  %s",
			seedHooks["SessionStart"], outHooks["SessionStart"])
	}

	// The user Bash PreToolUse entry survives value-equal, INCLUDING its
	// unmodeled timeout + statusMessage — the load-bearing assertion.
	seedBash := findBashEntry(t, seed)
	outBash := findBashEntry(t, out)
	if outBash == nil {
		t.Fatalf("user Bash PreToolUse entry dropped after merge:\n%s", out)
	}
	if !jsonValueEqual(t, seedBash, outBash) {
		t.Errorf("user Bash entry not value-equal after merge:\nwant %s\ngot  %s", seedBash, outBash)
	}
	// Explicit field-level checks: the inspection structs do NOT model these,
	// so their survival proves the raw-bytes passthrough.
	assertHookField(t, outBash, "timeout", float64(60))
	assertHookField(t, outBash, "statusMessage", "running bash guard")

	// Every managed entry is present, exactly once each.
	if n := countManagedEntries(t, out); n != wantManagedEntries() {
		t.Errorf("managed entry count = %d, want %d:\n%s", n, wantManagedEntries(), out)
	}
}

// TestMergeClaudeSettings_Idempotent: merging the same logical content twice
// is stable from the second application onward — merge(merge(x)) == merge(x)
// byte-for-byte (a first merge against a hand-formatted file legitimately
// re-indents, so the invariant is "stable from the 2nd application", not
// "merge never changes bytes"). Exactly one managed entry throughout.
func TestMergeClaudeSettings_Idempotent(t *testing.T) {
	once, err := mergeClaudeSettings(nil, hookAsset())
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	twice, err := mergeClaudeSettings(once, hookAsset())
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if !bytes.Equal(once, twice) {
		t.Errorf("not idempotent from 2nd application:\n--once--\n%s\n--twice--\n%s", once, twice)
	}
	if n := countManagedEntries(t, twice); n != wantManagedEntries() {
		t.Errorf("managed entry count = %d, want %d", n, wantManagedEntries())
	}
}

// TestMergeClaudeSettings_ReplacesStale: a settings.json already carrying a
// managed entry with a STALE command (matcher + marker but old reason text)
// is updated IN PLACE — after merge there is still exactly one managed entry
// and its command equals the current asset's (no duplicate).
func TestMergeClaudeSettings_ReplacesStale(t *testing.T) {
	stale := []byte(`{"hooks":{"PreToolUse":[` +
		`{"matcher":"mcp__knowledge__collect","hooks":[{"type":"command","command":": knowledge-managed:promote-guard STALE OLD REASON"}]}` +
		`]}}`)
	out, err := mergeClaudeSettings(stale, hookAsset())
	if err != nil {
		t.Fatalf("merge stale: %v", err)
	}
	if bytes.Contains(out, []byte("STALE OLD REASON")) {
		t.Errorf("stale managed command survived:\n%s", out)
	}
	if n := countManagedEntries(t, out); n != wantManagedEntries() {
		t.Errorf("managed entry count = %d, want %d (in-place replace):\n%s", n, wantManagedEntries(), out)
	}
	// The promote-guard entry's command equals the current asset's command.
	wantCmd := assetCommand(t, promoteGuardEntry(t))
	gotCmd := assetCommand(t, findManagedEntry(t, out))
	if gotCmd != wantCmd {
		t.Errorf("managed command not refreshed to current asset:\nwant %q\ngot  %q", wantCmd, gotCmd)
	}
}

// TestPromoteGuardCommand_ParamConditional is the headline criterion: the
// emitted hook command must gate ONLY on promote==true. It executes the
// command (extracted from assets.ClaudeHooks) via `bash -c`, feeding the
// three tool_input shapes on stdin. Guarded by a Short skip and a bash+jq
// PATH check so it is hermetic where those binaries are unavailable
// (mirroring the PATH-tool pattern at config/autodetect_test.go:346).
func TestPromoteGuardCommand_ParamConditional(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bash/jq exec test in -short mode")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH; skipping param-conditional exec test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH; skipping param-conditional exec test")
	}

	command := assetCommand(t, promoteGuardEntry(t))

	run := func(stdin string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", "-c", command)
		cmd.Stdin = bytes.NewBufferString(stdin)
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		code := 0
		if err != nil {
			if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("exec hook command: %v", err)
			}
		}
		return out.String(), code
	}

	// promote:true → permissionDecision "ask".
	stdout, code := run(`{"tool_input":{"promote":true}}`)
	if code != 0 {
		t.Errorf("promote:true exit = %d, want 0", code)
	}
	var decision struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &decision); err != nil {
		t.Fatalf("promote:true stdout is not JSON: %v\nstdout=%q", err, stdout)
	}
	if decision.HookSpecificOutput.PermissionDecision != "ask" {
		t.Errorf("promote:true permissionDecision = %q, want \"ask\"\nstdout=%s",
			decision.HookSpecificOutput.PermissionDecision, stdout)
	}

	// promote:false → no output, exit 0.
	stdout, code = run(`{"tool_input":{"promote":false}}`)
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Errorf("promote:false → stdout=%q exit=%d, want empty/0", stdout, code)
	}

	// promote absent → no output, exit 0.
	stdout, code = run(`{"tool_input":{}}`)
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Errorf("promote absent → stdout=%q exit=%d, want empty/0", stdout, code)
	}
}

// TestCheckClaudeSettings drives checkClaudeSettings against a scratch home for
// each of the three outcomes (modeled on TestCheckClaudeMD): missing
// settings.json → warn; after writeClaudeSettings → ok; drifted (managed
// entry edited) → warn.
func TestCheckClaudeSettings(t *testing.T) {
	home := t.TempDir()
	setBootstrapHome(t, home)

	// Missing settings.json → warn.
	if got := checkClaudeSettings(graphclient.DefaultMCPHTTPPort, true); got.status != statusWarn {
		t.Errorf("missing settings.json: status=%v, want warn", got.status)
	}

	// In-sync → ok.
	path := filepath.Join(home, ".claude", "settings.json")
	if _, err := writeClaudeSettings(path, hookAsset(), false); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	if got := checkClaudeSettings(graphclient.DefaultMCPHTTPPort, true); got.status != statusOK {
		t.Errorf("in-sync settings.json: status=%v, want ok (msg=%q)", got.status, got.msg)
	}

	// Drifted (managed entry's command edited) → warn.
	drift := []byte(`{"hooks":{"PreToolUse":[{"matcher":"mcp__knowledge__collect","hooks":[{"type":"command","command":": knowledge-managed:promote-guard DRIFTED"}]}]}}`)
	if err := os.WriteFile(path, drift, 0o600); err != nil {
		t.Fatalf("drift settings.json: %v", err)
	}
	if got := checkClaudeSettings(graphclient.DefaultMCPHTTPPort, true); got.status != statusWarn {
		t.Errorf("drifted settings.json: status=%v, want warn", got.status)
	}
}

// --- test helpers ---

// jsonValueEqual reports whether two JSON byte slices are structurally equal
// (semantic value-equality, independent of key order / whitespace).
func jsonValueEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("decode a: %v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("decode b: %v\n%s", err, b)
	}
	return reflect.DeepEqual(av, bv)
}

// findBashEntry returns the raw PreToolUse entry whose matcher is "Bash"
// (the user-authored hook the non-clobber tests seed), or nil when none
// matches.
func findBashEntry(t *testing.T, doc []byte) json.RawMessage {
	t.Helper()
	for _, e := range decodePreToolUse(t, doc) {
		var head struct {
			Matcher string `json:"matcher"`
		}
		if err := json.Unmarshal(e, &head); err != nil {
			continue
		}
		if head.Matcher == "Bash" {
			return e
		}
	}
	return nil
}

// findManagedEntry returns the raw PROMOTE-GUARD managed PreToolUse entry from
// doc. The managed set holds more than one entry now, so the lookup is
// by-matcher rather than first-managed-wins.
func findManagedEntry(t *testing.T, doc []byte) json.RawMessage {
	t.Helper()
	for _, e := range decodePreToolUse(t, doc) {
		if m, ok := managedEntryMatcher(e, mustCanonicalEntries(t)); ok && m == knowledgeHookMatcher {
			return e
		}
	}
	t.Fatalf("no promote-guard managed entry in:\n%s", doc)
	return nil
}

// assetCommand extracts hooks[0].command from a single matcher-entry JSON
// (the asset shape, or a single PreToolUse entry).
func assetCommand(t *testing.T, entry []byte) string {
	t.Helper()
	var e struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		t.Fatalf("decode entry for command: %v\n%s", err, entry)
	}
	if len(e.Hooks) == 0 {
		t.Fatalf("entry has no hooks:\n%s", entry)
	}
	return e.Hooks[0].Command
}

// assertHookField decodes the single hook-command object inside a PreToolUse
// entry and asserts key==want (value-equal). Used to prove unmodeled fields
// (timeout, statusMessage) survived the merge.
func assertHookField(t *testing.T, entry json.RawMessage, key string, want any) {
	t.Helper()
	var e struct {
		Hooks []map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		t.Fatalf("decode entry for field %q: %v\n%s", key, err, entry)
	}
	if len(e.Hooks) == 0 {
		t.Fatalf("entry has no hooks for field %q:\n%s", key, entry)
	}
	got, ok := e.Hooks[0][key]
	if !ok {
		t.Errorf("hook field %q dropped:\n%s", key, entry)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hook field %q = %v (%T), want %v (%T)", key, got, got, want, want)
	}
}
