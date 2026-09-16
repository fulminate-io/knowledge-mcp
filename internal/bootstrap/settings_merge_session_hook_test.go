// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// managedMatchers returns the matchers of every knowledge-managed PreToolUse
// entry in doc, in file order. It asks the PRODUCTION classifier, so a test
// that pins the managed SET cannot drift from what the merge actually treats as
// managed.
func managedMatchers(t *testing.T, doc []byte) []string {
	t.Helper()
	var out []string
	for _, e := range decodePreToolUse(t, doc) {
		if m, ok := managedEntryMatcher(e, mustCanonicalEntries(t)); ok {
			out = append(out, m)
		}
	}
	return out
}

// entryByMatcher returns the raw PreToolUse entry with the given matcher.
func entryByMatcher(t *testing.T, doc []byte, matcher string) json.RawMessage {
	t.Helper()
	for _, e := range decodePreToolUse(t, doc) {
		var head struct {
			Matcher string `json:"matcher"`
		}
		if err := json.Unmarshal(e, &head); err != nil {
			continue
		}
		if head.Matcher == matcher {
			return e
		}
	}
	return nil
}

// TestMergeClaudeSettings_InstallsBothManagedEntries is the headline sentinel
// widening: the merge installs a SET of managed entries — the promote-guard
// command hook and the http session hook — not one.
//
// It reads the expected matchers off the ASSET rather than spelling them, so
// the pin states "every entry the asset ships is installed" rather than a
// literal pair that would have to be edited alongside the asset.
func TestMergeClaudeSettings_InstallsBothManagedEntries(t *testing.T) {
	asset := renderedHookAsset(t, graphclient.DefaultMCPHTTPPort)
	want, err := canonicalManagedEntries(asset)
	if err != nil {
		t.Fatalf("canonicalManagedEntries: %v", err)
	}
	if len(want) < 2 {
		t.Fatalf("the hook asset ships %d entries, want the promote-guard AND the session hook", len(want))
	}

	out, err := mergeClaudeSettings(nil, asset)
	if err != nil {
		t.Fatalf("mergeClaudeSettings: %v", err)
	}
	got := managedMatchers(t, out)
	if len(got) != len(want) {
		t.Fatalf("managed entries after merge = %v, want %d (one per asset entry):\n%s", got, len(want), out)
	}
	for i, w := range want {
		if got[i] != w.matcher {
			t.Errorf("managed entry %d matcher = %q, want %q", i, got[i], w.matcher)
		}
	}
}

// TestSessionHookEntry_ShapeAndMarker pins the session hook's own shape: an
// `http` handler (Claude's only self-POSTing handler type), a url on the
// daemon's hook path, and the managed marker on a HEADER — because an http hook
// object has no `command` field to carry one, which is the whole reason the
// sentinel had to widen.
func TestSessionHookEntry_ShapeAndMarker(t *testing.T) {
	out, err := mergeClaudeSettings(nil, renderedHookAsset(t, graphclient.DefaultMCPHTTPPort))
	if err != nil {
		t.Fatalf("mergeClaudeSettings: %v", err)
	}
	entry := entryByMatcher(t, out, knowledgeSessionHookMatcher)
	if entry == nil {
		t.Fatalf("no entry with matcher %q:\n%s", knowledgeSessionHookMatcher, out)
	}
	var decoded preToolUseEntry
	if err := json.Unmarshal(entry, &decoded); err != nil {
		t.Fatalf("decode session entry: %v", err)
	}
	if len(decoded.Hooks) != 1 {
		t.Fatalf("session entry carries %d hooks, want 1:\n%s", len(decoded.Hooks), entry)
	}
	h := decoded.Hooks[0]
	if h.Type != "http" {
		t.Errorf("session hook type = %q, want \"http\" (the only Claude handler that POSTs by itself)", h.Type)
	}
	if h.Command != "" {
		t.Errorf("session hook carries a command %q; an http hook has no command field", h.Command)
	}
	if want := "/hook/claude"; !strings.Contains(h.URL, want) {
		t.Errorf("session hook url = %q, want it to name the daemon path %q", h.URL, want)
	}
	if got := h.Headers[knowledgeManagedHeader]; got != knowledgeSessionHookMarker {
		t.Errorf("session hook header %s = %q, want %q — the marker is what classifies it as managed",
			knowledgeManagedHeader, got, knowledgeSessionHookMarker)
	}
}

// TestMergeClaudeSettings_NonClobberInputClasses runs the merge over the five
// PreToolUse shapes a user's file can be in, asserting in every one that the
// user's own content survives and that the managed set collapses to exactly one
// entry per matcher.
func TestMergeClaudeSettings_NonClobberInputClasses(t *testing.T) {
	asset := renderedHookAsset(t, graphclient.DefaultMCPHTTPPort)
	managed, err := canonicalManagedEntries(asset)
	if err != nil {
		t.Fatalf("canonicalManagedEntries: %v", err)
	}
	userEntry := `{"matcher":"Bash","hooks":[{"type":"command","command":"echo user","timeout":7}]}`
	dupGuard := `{"matcher":"mcp__knowledge__collect","hooks":[{"type":"command","command":": knowledge-managed:promote-guard OLD"}]}`
	dupSession := `{"matcher":"mcp__knowledge__.*","hooks":[{"type":"http","url":"http://127.0.0.1:1/hook/claude",` +
		`"headers":{"X-Knowledge-Managed":"knowledge-managed:session-hook"}}]}`

	// A managed-MARKED entry whose matcher the asset no longer ships, and a
	// USER entry sitting on our own session matcher with no marker. These are
	// the two sides of the marker-only classifier: the first must be DROPPED
	// (it is ours, and nothing is left to refresh or remove it), the second
	// must SURVIVE (it is the user's, whatever matcher they chose).
	userOnOurMatcher := `{"matcher":"mcp__knowledge__.*","hooks":[{"type":"command",` +
		`"command":"echo mine","timeout":7}]}`
	// A user who LIFTED our promote-guard, marker and all, and re-pointed it at
	// another tool. Our exact marker on a matcher we do not ship: theirs.
	userCopyOfOurGuard := `{"matcher":"mcp__knowledge__search","hooks":[{"type":"command",` +
		`"command":"bash -c ': knowledge-managed:promote-guard; echo my own guard'"}]}`
	// A marker of the user's OWN invention under our prefix: also theirs.
	userOwnMarker := `{"matcher":"Write","hooks":[{"type":"command",` +
		`"command":": knowledge-managed:my-own-thing; echo mine"}]}`

	for _, tc := range []struct {
		name        string
		seed        string
		wantUser    bool
		wantAbsent  []string
		wantPresent []string
	}{
		{name: "no hooks key at all", seed: `{"model":"opus"}`},
		{name: "other events only", seed: `{"hooks":{"SessionStart":[{"matcher":"","hooks":[]}]}}`},
		{name: "user entries only", seed: `{"hooks":{"PreToolUse":[` + userEntry + `]}}`, wantUser: true},
		{
			name:     "managed entries in reverse order",
			seed:     `{"hooks":{"PreToolUse":[` + dupSession + `,` + userEntry + `,` + dupGuard + `]}}`,
			wantUser: true,
		},
		{
			name: "a stale duplicate of each managed entry",
			seed: `{"hooks":{"PreToolUse":[` + dupGuard + `,` + dupSession + `,` + userEntry + `,` +
				dupGuard + `,` + dupSession + `]}}`,
			wantUser: true,
		},
		{
			name:        "a user's COPY of our promote-guard, re-pointed at another tool",
			seed:        `{"hooks":{"PreToolUse":[` + userCopyOfOurGuard + `,` + userEntry + `]}}`,
			wantUser:    true,
			wantPresent: []string{"mcp__knowledge__search", "echo my own guard"},
		},
		{
			name:        "a marker of the user's own invention under our prefix",
			seed:        `{"hooks":{"PreToolUse":[` + userOwnMarker + `,` + userEntry + `]}}`,
			wantUser:    true,
			wantPresent: []string{"knowledge-managed:my-own-thing", "echo mine"},
		},
		{
			name:        "a USER entry on our own session matcher, carrying no marker",
			seed:        `{"hooks":{"PreToolUse":[` + userOnOurMatcher + `,` + userEntry + `]}}`,
			wantUser:    true,
			wantPresent: []string{"echo mine"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := mergeClaudeSettings([]byte(tc.seed), asset)
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			got := managedMatchers(t, out)
			if len(got) != len(managed) {
				t.Errorf("managed entries = %v, want exactly %d (one per matcher):\n%s", got, len(managed), out)
			}
			if tc.wantUser && findBashEntry(t, out) == nil {
				t.Errorf("the user's Bash entry was dropped:\n%s", out)
			}
			if tc.wantUser {
				assertHookField(t, findBashEntry(t, out), "timeout", float64(7))
			}
			for _, want := range tc.wantAbsent {
				if strings.Contains(string(out), want) {
					t.Errorf("a retired knowledge-managed entry survived the merge (%q still present):\n%s", want, out)
				}
			}
			for _, want := range tc.wantPresent {
				if !strings.Contains(string(out), want) {
					t.Errorf("a user entry on our own matcher was dropped (%q absent):\n%s", want, out)
				}
			}
		})
	}
}

// TestRenderClaudeHooks_PortMatrix is the --mcp-port matrix at the RENDER seam:
// every in-range port lands verbatim in the session hook's url, and both
// out-of-range bounds refuse with errMCPPortRange rather than rendering
// something.
func TestRenderClaudeHooks_PortMatrix(t *testing.T) {
	for _, port := range []int{graphclient.DefaultMCPHTTPPort, 20000, mcpPortMin, mcpPortMax} {
		t.Run("in range "+strconv.Itoa(port), func(t *testing.T) {
			rendered, err := renderClaudeHooks(assets.ClaudeHooks, port)
			if err != nil {
				t.Fatalf("renderClaudeHooks(%d): %v", port, err)
			}
			if strings.Contains(string(rendered), claudeHookPortPlaceholder) {
				t.Errorf("rendered asset still carries the %s placeholder", claudeHookPortPlaceholder)
			}
			want := "http://127.0.0.1:" + strconv.Itoa(port) + "/hook/claude"
			if !strings.Contains(string(rendered), want) {
				t.Errorf("rendered asset does not name %q:\n%s", want, rendered)
			}
		})
	}
	for _, port := range []int{mcpPortMin - 1, mcpPortMax + 1} {
		t.Run("out of range "+strconv.Itoa(port), func(t *testing.T) {
			if _, err := renderClaudeHooks(assets.ClaudeHooks, port); !errors.Is(err, errMCPPortRange) {
				t.Errorf("renderClaudeHooks(%d) error = %v, want errMCPPortRange", port, err)
			}
		})
	}
}

// TestRenderClaudeHooks_RefusesAnAssetWithNoPlaceholder is the kill for the
// render itself: an asset that stopped carrying the placeholder would render to
// whatever literal port an editor left behind and every install would point at
// one machine's daemon. It must refuse, loudly, rather than pass the bytes
// through.
func TestRenderClaudeHooks_RefusesAnAssetWithNoPlaceholder(t *testing.T) {
	flat := []byte(`[{"matcher":"x","hooks":[{"type":"http","url":"http://127.0.0.1:15023/hook/claude"}]}]`)
	_, err := renderClaudeHooks(flat, graphclient.DefaultMCPHTTPPort)
	if !errors.Is(err, errNoHookPortPlaceholder) {
		t.Fatalf("renderClaudeHooks(no placeholder) error = %v, want errNoHookPortPlaceholder", err)
	}
}

// TestClaudeHookPortFromSettings round-trips the port the doctor drift check
// depends on: the port an installed settings.json names is read back out of it.
// A file with no managed session entry reports a MISS rather than a default, so
// the caller decides what an absent entry means.
func TestClaudeHookPortFromSettings(t *testing.T) {
	const port = 20001
	installed, err := mergeClaudeSettings(nil, renderedHookAsset(t, port))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	got, ok := claudeHookPortFromSettings(installed)
	if !ok || got != port {
		t.Errorf("claudeHookPortFromSettings = (%d, %v), want (%d, true)", got, ok, port)
	}
	if _, ok := claudeHookPortFromSettings([]byte(`{"hooks":{"PreToolUse":[]}}`)); ok {
		t.Error("claudeHookPortFromSettings reported a port for a file with no managed session hook")
	}
}

// TestCheckClaudeSettings_PortRendered is the DOCTOR DRIFT CELL. An install at a
// NON-DEFAULT --mcp-port must still read as in sync: the comparison renders at
// the port the file itself names, so a user on any port is not warned forever.
// The mutation that reds it is rendering the doctor's comparison bytes at the
// default port while the file was written at another.
func TestCheckClaudeSettings_PortRendered(t *testing.T) {
	home := t.TempDir()
	setBootstrapHome(t, home)
	path := filepath.Join(home, ".claude", "settings.json")

	const port = 20002
	if got := checkClaudeSettings(port, true); got.status != statusWarn {
		t.Errorf("missing settings.json: status=%v, want warn", got.status)
	}

	if _, err := writeClaudeSettings(path, renderedHookAsset(t, port), false); err != nil {
		t.Fatalf("seed settings.json at port %d: %v", port, err)
	}
	if got := checkClaudeSettings(port, true); got.status != statusOK {
		t.Errorf("settings.json installed at non-default port %d: status=%v msg=%q, want ok",
			port, got.status, got.msg)
	}

	// THE WRONG-PORT ARM: the same in-sync file, checked by a daemon serving a
	// DIFFERENT port. The hook posts where nothing listens, so every session
	// resolves to none — the state this diagnostic exists to catch, and the one
	// it reported as "in sync" before.
	const otherPort = 20099
	got := checkClaudeSettings(otherPort, true)
	if got.status != statusWarn {
		t.Errorf("hook at port %d checked by a daemon on %d: status=%v msg=%q, want warn",
			port, otherPort, got.status, got.msg)
	}
	for _, want := range []string{strconv.Itoa(port), strconv.Itoa(otherPort)} {
		if !strings.Contains(got.msg, want) {
			t.Errorf("wrong-port warning %q does not name port %s", got.msg, want)
		}
	}
	if !strings.Contains(got.detail, "--mcp-port "+strconv.Itoa(otherPort)) {
		t.Errorf("wrong-port remediation %q does not name the daemon's port", got.detail)
	}

	// THE POLL PATH. The in-daemon manage(status) doctor block does not know
	// which port this process serves and passes UNKNOWN. A correctly installed
	// non-default hook must read statusOK there: comparing it against a default
	// would warn on a working install and tell the user to reinstall the hook at
	// a port nothing serves. Shape drift is still reported; the port is not
	// judged at all.
	pollPort, pollKnown := (&client{}).mcpHTTPPort()
	if pollKnown {
		t.Fatalf("the poll path claims to know the served port (%d); this row exists because it does not", pollPort)
	}
	if poll := checkClaudeSettings(pollPort, pollKnown); poll.status != statusOK {
		t.Errorf("poll path on a correct install at port %d: status=%v msg=%q detail=%q, want ok",
			port, poll.status, poll.msg, poll.detail)
	}

	// Hand-edit the managed entry → drift, whatever the port.
	edited := strings.Replace(string(mustRead(t, path)), "knowledge-managed:promote-guard", "knowledge-managed:promote-guard EDITED", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("drift settings.json: %v", err)
	}
	if got := checkClaudeSettings(port, true); got.status != statusWarn {
		t.Errorf("hand-edited managed entry: status=%v, want warn", got.status)
	}
}

// renderedHookAsset renders the embedded asset at port, failing the test on a
// render error.
func renderedHookAsset(t *testing.T, port int) []byte {
	t.Helper()
	rendered, err := renderClaudeHooks(assets.ClaudeHooks, port)
	if err != nil {
		t.Fatalf("renderClaudeHooks(%d): %v", port, err)
	}
	return rendered
}

// mustRead reads path or fails the test.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // path is under t.TempDir()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// TestSpliceManagedEntries_DropsARetiredMarker is the retirement half of the
// classifier, and it needs its own test because nothing is retired TODAY: a
// retired marker is one that is still in knowledgeHookMarkers and no longer in
// the asset, so the state is produced here by handing the splice a canonical
// set that ships only the promote-guard while the session marker stays in the
// marker list.
//
// A retired entry is ours on ANY matcher — only we ever wrote that marker and
// there is no current entry it could be a copy of — so it is removed, while the
// user's own entry beside it survives. Removing the marker from
// knowledgeHookMarkers instead of leaving it there reclassifies the entry as
// user content and strands it in the file forever, which is what the list's
// doc comment warns about.
func TestSpliceManagedEntries_DropsARetiredMarker(t *testing.T) {
	all, err := canonicalManagedEntries(hookAsset())
	if err != nil {
		t.Fatalf("canonicalManagedEntries: %v", err)
	}
	var shippedNow []managedEntry
	for _, m := range all {
		if m.marker != knowledgeSessionHookMarker {
			shippedNow = append(shippedNow, m)
		}
	}
	if len(shippedNow) == len(all) {
		t.Fatalf("the asset ships no entry carrying %q; this test's retirement is not simulated", knowledgeSessionHookMarker)
	}

	// The retired entry, on a matcher the trimmed asset does not ship.
	retired := json.RawMessage(`{"matcher":"mcp__knowledge__retired","hooks":[{"type":"http",` +
		`"url":"http://127.0.0.1:15023/hook/claude",` +
		`"headers":{"X-Knowledge-Managed":"` + knowledgeSessionHookMarker + `"}}]}`)
	user := json.RawMessage(`{"matcher":"Bash","hooks":[{"type":"command","command":"echo user"}]}`)

	out := spliceManagedEntries([]json.RawMessage{retired, user}, shippedNow)

	var body strings.Builder
	for _, e := range out {
		body.WriteString(string(e))
	}
	if strings.Contains(body.String(), "mcp__knowledge__retired") {
		t.Errorf("the retired managed entry survived the splice:\n%s", body.String())
	}
	if !strings.Contains(body.String(), "echo user") {
		t.Errorf("the user's own entry was dropped beside it:\n%s", body.String())
	}
}
