// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// The registered MCP url both installer verbs put on the wire, written as a
// LITERAL rather than composed from daemonMCPURL: the argv these tests assert
// is the observable the launcher and the operator see, so its expectation
// must not come from the producer under test.
const (
	wantDefaultMCPURL = "http://127.0.0.1:15023/mcp"
	wantFlaggedMCPURL = "http://127.0.0.1:61723/mcp"
	flaggedMCPPort    = "61723"
)

// claudeAddArgvFor returns the exact space-joined argv line the recording fake
// captures for `claude mcp add-json` at url.
func claudeAddArgvFor(url string) string {
	return `mcp add-json -s user knowledge {"type":"http","url":"` + url + `","timeout":180000}`
}

// codexAddArgvFor returns the exact space-joined argv line the recording fake
// captures for `codex mcp add` at url.
func codexAddArgvFor(url string) string {
	return "mcp add knowledge --url " + url
}

// installClaudeHarness puts a recording `claude` fake on PATH under an
// isolated HOME and returns the argv log path, the scratch dest flags the
// verb must be driven with, and the paths those flags name. destPaths is
// returned rather than re-derived from destArgs so an assertion about what
// was written never depends on which of the flags happen to take a value.
// Setup only — it fails the test just for an environment problem, never for
// an expectation. The registration EXECs the client binary, so the fake is
// the only place this may run: a real claude on PATH would write the
// operator's own ~/.claude.json.
func installClaudeHarness(t *testing.T) (logPath string, destArgs, destPaths []string) {
	t.Helper()
	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "argv.log")
	writeRecordingFake(t, binDir, "claude", logPath)
	withPATH(t, binDir)
	withHOME(t, t.TempDir())
	withStubExecutable(t, "/opt/knowledge/bin/knowledge")

	destRoot := t.TempDir()
	dotClaude := filepath.Join(destRoot, "dotclaude")
	claudeMD := filepath.Join(destRoot, "CLAUDE.md")
	settings := filepath.Join(destRoot, "settings.json")
	return logPath,
		[]string{"--dest", dotClaude, "--claude-md-dest", claudeMD, "--claude-settings-dest", settings},
		[]string{dotClaude, claudeMD, settings}
}

// installCodexHarness is installClaudeHarness's codex twin: a recording
// `codex` fake on PATH, an isolated HOME (the registration also patches
// <home>/.codex/config.toml) and scratch split-root dest flags.
func installCodexHarness(t *testing.T) (logPath string, destArgs, destPaths []string) {
	t.Helper()
	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "argv.log")
	writeRecordingFake(t, binDir, "codex", logPath)
	withPATH(t, binDir)
	withHOME(t, t.TempDir())

	destRoot := t.TempDir()
	skills := filepath.Join(destRoot, "skills")
	agents := filepath.Join(destRoot, "agents")
	agentsMD := filepath.Join(destRoot, "AGENTS.md")
	return logPath,
		[]string{"--skills-dest", skills, "--agents-dest", agents, "--agents-md-dest", agentsMD},
		[]string{skills, agents, agentsMD}
}

// addArgvLine returns the `mcp add...` line from a captured remove+add pair,
// or an error describing what was captured instead. It decides nothing about
// the test: the caller reports the failure, keeping the message next to the
// case that produced it.
func addArgvLine(lines []string) (string, error) {
	if len(lines) != 2 {
		return "", fmt.Errorf("recorded %d argv lines, want 2 (remove + add): %v", len(lines), lines)
	}
	if lines[0] != "mcp remove knowledge" {
		return "", fmt.Errorf("first argv = %q, want %q", lines[0], "mcp remove knowledge")
	}
	return lines[1], nil
}

// TestRunInstallClaudeAssets_MCPPortFlagArgv (R1, claude): --mcp-port <n>
// makes the registered url name <n>, in the add-json config the verb hands
// the claude CLI.
func TestRunInstallClaudeAssets_MCPPortFlagArgv(t *testing.T) {
	log, dest, _ := installClaudeHarness(t)
	args := append(dest, "--mcp-port", flaggedMCPPort) //nolint:gocritic // deliberate copy: dest is reused per row
	if err := runInstallClaudeAssets(args); err != nil {
		t.Fatalf("runInstallClaudeAssets(--mcp-port %s) = %v, want nil", flaggedMCPPort, err)
	}
	add, err := addArgvLine(recordedLines(t, log))
	if err != nil {
		t.Fatalf("runInstallClaudeAssets(--mcp-port %s): %v", flaggedMCPPort, err)
	}
	if want := claudeAddArgvFor(wantFlaggedMCPURL); add != want {
		t.Errorf("add argv = %q, want %q", add, want)
	}
}

// TestRunInstallCodexAssets_MCPPortFlagArgv (R1, codex): the same for the
// codex url-form add.
func TestRunInstallCodexAssets_MCPPortFlagArgv(t *testing.T) {
	log, dest, _ := installCodexHarness(t)
	args := append(dest, "--mcp-port", flaggedMCPPort) //nolint:gocritic // deliberate copy: dest is reused per row
	if err := runInstallCodexAssets(args); err != nil {
		t.Fatalf("runInstallCodexAssets(--mcp-port %s) = %v, want nil", flaggedMCPPort, err)
	}
	add, err := addArgvLine(recordedLines(t, log))
	if err != nil {
		t.Fatalf("runInstallCodexAssets(--mcp-port %s): %v", flaggedMCPPort, err)
	}
	if want := codexAddArgvFor(wantFlaggedMCPURL); add != want {
		t.Errorf("add argv = %q, want %q", add, want)
	}
}

// mcpPortAcceptedRows are the two ENDPOINTS of the accepted range. R1 names
// 1024..65534 inclusive (the launcher's own bound), so both endpoints must
// register, not merely fail to be refused: each row asserts the argv the verb
// put on the wire names that exact port. Without these rows a comparison
// widened to `<=` / `>=` — which refuses both endpoints — leaves the suite
// green.
var mcpPortAcceptedRows = []struct {
	name  string
	value string
	url   string
}{
	{name: "at-lower-bound", value: "1024", url: "http://127.0.0.1:1024/mcp"},
	{name: "at-upper-bound", value: "65534", url: "http://127.0.0.1:65534/mcp"},
}

// TestRunInstallClaudeAssets_MCPPortBoundariesAccepted (R1, claude).
func TestRunInstallClaudeAssets_MCPPortBoundariesAccepted(t *testing.T) {
	for _, row := range mcpPortAcceptedRows {
		t.Run(row.name, func(t *testing.T) {
			log, dest, _ := installClaudeHarness(t)
			args := append(dest, "--mcp-port", row.value) //nolint:gocritic // deliberate copy
			if err := runInstallClaudeAssets(args); err != nil {
				t.Fatalf("runInstallClaudeAssets(--mcp-port %s) = %v, want nil (the bound is inclusive)", row.value, err)
			}
			add, err := addArgvLine(recordedLines(t, log))
			if err != nil {
				t.Fatalf("runInstallClaudeAssets(--mcp-port %s): %v", row.value, err)
			}
			if want := claudeAddArgvFor(row.url); add != want {
				t.Errorf("add argv = %q, want %q", add, want)
			}
		})
	}
}

// TestRunInstallCodexAssets_MCPPortBoundariesAccepted (R1, codex).
func TestRunInstallCodexAssets_MCPPortBoundariesAccepted(t *testing.T) {
	for _, row := range mcpPortAcceptedRows {
		t.Run(row.name, func(t *testing.T) {
			log, dest, _ := installCodexHarness(t)
			args := append(dest, "--mcp-port", row.value) //nolint:gocritic // deliberate copy
			if err := runInstallCodexAssets(args); err != nil {
				t.Fatalf("runInstallCodexAssets(--mcp-port %s) = %v, want nil (the bound is inclusive)", row.value, err)
			}
			add, err := addArgvLine(recordedLines(t, log))
			if err != nil {
				t.Fatalf("runInstallCodexAssets(--mcp-port %s): %v", row.value, err)
			}
			if want := codexAddArgvFor(row.url); add != want {
				t.Errorf("add argv = %q, want %q", add, want)
			}
		})
	}
}

// TestRunInstallClaudeAssets_DefaultMCPPortArgvUnchanged (R2, claude): absent
// the flag the argv is byte-identical to the pre-flag tree's — the literal
// default url, not a value derived from the code under test.
func TestRunInstallClaudeAssets_DefaultMCPPortArgvUnchanged(t *testing.T) {
	log, dest, _ := installClaudeHarness(t)
	if err := runInstallClaudeAssets(dest); err != nil {
		t.Fatalf("runInstallClaudeAssets(no --mcp-port) = %v, want nil", err)
	}
	add, err := addArgvLine(recordedLines(t, log))
	if err != nil {
		t.Fatalf("runInstallClaudeAssets(no --mcp-port): %v", err)
	}
	if want := claudeAddArgvFor(wantDefaultMCPURL); add != want {
		t.Errorf("add argv = %q, want %q", add, want)
	}
}

// TestRunInstallCodexAssets_DefaultMCPPortArgvUnchanged (R2, codex).
func TestRunInstallCodexAssets_DefaultMCPPortArgvUnchanged(t *testing.T) {
	log, dest, _ := installCodexHarness(t)
	if err := runInstallCodexAssets(dest); err != nil {
		t.Fatalf("runInstallCodexAssets(no --mcp-port) = %v, want nil", err)
	}
	add, err := addArgvLine(recordedLines(t, log))
	if err != nil {
		t.Fatalf("runInstallCodexAssets(no --mcp-port): %v", err)
	}
	if want := codexAddArgvFor(wantDefaultMCPURL); add != want {
		t.Errorf("add argv = %q, want %q", add, want)
	}
}

// mcpPortRefusalRows are the malformed values R3 names. Each must refuse
// loudly naming the flag, substitute no default, and reach neither a write
// nor an exec. wantRange marks the two arms the verb's own range check
// refuses (the other two never get past flag's numeric parse, which is what
// refuses them and which names the flag but not the bounds).
var mcpPortRefusalRows = []struct {
	name      string
	value     string
	wantRange bool
}{
	{name: "non-numeric", value: "abc"},
	{name: "empty-when-set", value: ""},
	{name: "below-range", value: "1023", wantRange: true},
	{name: "above-range", value: "65535", wantRange: true},
}

// mcpPortArms are the three arms every refusal row is driven on. R3 states
// the refusal as a property of the VALUE, scoped to no arm: --dry-run and
// --diff are read-only previews, and a preview that renders a would-run line
// naming a port it was just told is invalid is the defect this axis catches.
var mcpPortArms = []struct {
	name  string
	extra []string
}{
	{name: "write", extra: nil},
	{name: "dry-run", extra: []string{"--dry-run"}},
	{name: "diff", extra: []string{"--diff"}},
}

// checkMCPPortRefusal returns every way one refusal arm fell short: no
// error, an error that does not name the flag, a range refusal that does not
// carry the sentinel or the bounds, a destination that was written, or an
// exec that happened. It reports nothing itself — the caller decides.
func checkMCPPortRefusal(err error, lines, destPaths []string, wantRange bool) []error {
	var problems []error
	if err == nil {
		return []error{errors.New("the value was accepted, want a refusal")}
	}
	// The refusal's MESSAGE is the requirement here — it is what the person
	// who typed the flag reads — so these assert the rendered text. The
	// CONDITION is recognized structurally, by errors.Is on the sentinel,
	// wherever a sentinel exists; the two parse arms are refused by `flag`
	// itself, which offers none.
	msg := err.Error()
	if !strings.Contains(msg, "mcp-port") {
		problems = append(problems, fmt.Errorf("refusal = %q, want it to name the flag", msg))
	}
	if wantRange {
		if !errors.Is(err, errMCPPortRange) {
			problems = append(problems, fmt.Errorf("refusal = %q, want it to wrap errMCPPortRange", msg))
		}
		for _, bound := range []string{"1024", "65534"} {
			if !strings.Contains(msg, bound) {
				problems = append(problems, fmt.Errorf("refusal = %q, want it to name the bound %s", msg, bound))
			}
		}
	}
	// The destinations' absence is what pins the refusal AHEAD of the asset
	// walk: a verb that validated late would already have written.
	for _, path := range destPaths {
		if _, statErr := os.Stat(path); statErr == nil {
			problems = append(problems, fmt.Errorf("wrote %s before refusing, want nothing written", path))
		}
	}
	if len(lines) != 0 {
		problems = append(problems, fmt.Errorf("exec'd the client: %v, want no exec", lines))
	}
	return problems
}

// TestRunInstallClaudeAssets_MCPPortRefusals (R3, claude): every malformed
// class on every arm.
func TestRunInstallClaudeAssets_MCPPortRefusals(t *testing.T) {
	for _, row := range mcpPortRefusalRows {
		for _, arm := range mcpPortArms {
			t.Run(row.name+"/"+arm.name, func(t *testing.T) {
				log, dest, destPaths := installClaudeHarness(t)
				args := append(append(dest, arm.extra...), "--mcp-port", row.value) //nolint:gocritic // deliberate copy
				err := runInstallClaudeAssets(args)
				for _, problem := range checkMCPPortRefusal(err, recordedLines(t, log), destPaths, row.wantRange) {
					t.Errorf("runInstallClaudeAssets(%v --mcp-port %q): %v", arm.extra, row.value, problem)
				}
			})
		}
	}
}

// TestRunInstallCodexAssets_MCPPortRefusals (R3, codex).
func TestRunInstallCodexAssets_MCPPortRefusals(t *testing.T) {
	for _, row := range mcpPortRefusalRows {
		for _, arm := range mcpPortArms {
			t.Run(row.name+"/"+arm.name, func(t *testing.T) {
				log, dest, destPaths := installCodexHarness(t)
				args := append(append(dest, arm.extra...), "--mcp-port", row.value) //nolint:gocritic // deliberate copy
				err := runInstallCodexAssets(args)
				for _, problem := range checkMCPPortRefusal(err, recordedLines(t, log), destPaths, row.wantRange) {
					t.Errorf("runInstallCodexAssets(%v --mcp-port %q): %v", arm.extra, row.value, problem)
				}
			})
		}
	}
}

// TestInstallAssetsMCPPortUsage (R3): the flag's own usage text names the
// accepted range on both verbs, which is what `flag` prints beside its parse
// refusal for the non-numeric and empty arms (those two never reach the
// range check), and what the generated docs table renders. The default is
// pinned to the daemon's default MCP port so R2's byte-identity is a
// property of the registration, not of an accident.
func TestInstallAssetsMCPPortUsage(t *testing.T) {
	claudeFS := flag.NewFlagSet("knowledge install-claude-assets", flag.ContinueOnError)
	var claudeFlags installAssetsFlags
	registerInstallClaudeFlags(claudeFS, &claudeFlags)

	codexFS := flag.NewFlagSet("knowledge install-codex-assets", flag.ContinueOnError)
	var codexFlags installCodexFlags
	registerInstallCodexFlags(codexFS, &codexFlags)

	for verb, fs := range map[string]*flag.FlagSet{"install-claude-assets": claudeFS, "install-codex-assets": codexFS} {
		f := fs.Lookup("mcp-port")
		if f == nil {
			t.Errorf("%s registers no --mcp-port flag", verb)
			continue
		}
		for _, bound := range []string{"1024", "65534"} {
			if !strings.Contains(f.Usage, bound) {
				t.Errorf("%s --mcp-port usage = %q, want it to name the bound %s", verb, f.Usage, bound)
			}
		}
		if want := strconv.Itoa(graphclient.DefaultMCPHTTPPort); f.DefValue != want {
			t.Errorf("%s --mcp-port default = %q, want %q", verb, f.DefValue, want)
		}
	}
}

// TestRunInstallClaudeAssets_DryRunRendersFlaggedURL (R4, claude): the
// dry-run arm renders the same url the write arm would put on the wire, and
// still execs nothing.
func TestRunInstallClaudeAssets_DryRunRendersFlaggedURL(t *testing.T) {
	log, dest, _ := installClaudeHarness(t)
	args := append(dest, "--dry-run", "--mcp-port", flaggedMCPPort) //nolint:gocritic // deliberate copy
	var err error
	out := captureStdout(t, func() { err = runInstallClaudeAssets(args) })
	if err != nil {
		t.Fatalf("runInstallClaudeAssets(--dry-run --mcp-port %s) = %v, want nil", flaggedMCPPort, err)
	}
	want := "would run: claude " + claudeAddArgvFor(wantFlaggedMCPURL)
	if !strings.Contains(out, want) {
		t.Errorf("dry-run output did not render %q; got:\n%s", want, out)
	}
	if strings.Contains(out, wantDefaultMCPURL) {
		t.Errorf("dry-run output still names the default url %q; got:\n%s", wantDefaultMCPURL, out)
	}
	if lines := recordedLines(t, log); len(lines) != 0 {
		t.Errorf("dry-run exec'd the client: %v, want no exec", lines)
	}
}

// TestRunInstallCodexAssets_DryRunRendersFlaggedURL (R4, codex).
func TestRunInstallCodexAssets_DryRunRendersFlaggedURL(t *testing.T) {
	log, dest, _ := installCodexHarness(t)
	args := append(dest, "--dry-run", "--mcp-port", flaggedMCPPort) //nolint:gocritic // deliberate copy
	var err error
	out := captureStdout(t, func() { err = runInstallCodexAssets(args) })
	if err != nil {
		t.Fatalf("runInstallCodexAssets(--dry-run --mcp-port %s) = %v, want nil", flaggedMCPPort, err)
	}
	want := "would run: codex " + codexAddArgvFor(wantFlaggedMCPURL)
	if !strings.Contains(out, want) {
		t.Errorf("dry-run output did not render %q; got:\n%s", want, out)
	}
	if strings.Contains(out, wantDefaultMCPURL) {
		t.Errorf("dry-run output still names the default url %q; got:\n%s", wantDefaultMCPURL, out)
	}
	if lines := recordedLines(t, log); len(lines) != 0 {
		t.Errorf("dry-run exec'd the client: %v, want no exec", lines)
	}
}

// checkDiffOutputHasNoURL returns the problems in a --diff render: an empty
// render (the zero would then prove nothing — the instrument must be shown
// to render at all in the same run) and any line naming a loopback url.
func checkDiffOutputHasNoURL(out string) []error {
	if !strings.Contains(out, "NEW: ") {
		return []error{fmt.Errorf("diff rendered nothing at all, so a url absence proves nothing; got:\n%s", out)}
	}
	var problems []error
	for ln := range strings.SplitSeq(out, "\n") {
		if strings.Contains(ln, "127.0.0.1") {
			problems = append(problems, fmt.Errorf("rendered an MCP url: %q", ln))
		}
	}
	return problems
}

// TestRunInstallAssets_DiffArmRendersNoMCPURL (R4, settlement 1): --diff
// stays a file-diff preview and renders NO url, with or without --mcp-port.
func TestRunInstallAssets_DiffArmRendersNoMCPURL(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		_, dest, _ := installClaudeHarness(t)
		args := append(dest, "--diff", "--mcp-port", flaggedMCPPort) //nolint:gocritic // deliberate copy
		var err error
		out := captureStdout(t, func() { err = runInstallClaudeAssets(args) })
		if err != nil {
			t.Fatalf("runInstallClaudeAssets(--diff) = %v, want nil", err)
		}
		for _, problem := range checkDiffOutputHasNoURL(out) {
			t.Errorf("runInstallClaudeAssets(--diff): %v", problem)
		}
	})
	t.Run("codex", func(t *testing.T) {
		_, dest, _ := installCodexHarness(t)
		args := append(dest, "--diff", "--mcp-port", flaggedMCPPort) //nolint:gocritic // deliberate copy
		var err error
		out := captureStdout(t, func() { err = runInstallCodexAssets(args) })
		if err != nil {
			t.Fatalf("runInstallCodexAssets(--diff) = %v, want nil", err)
		}
		for _, problem := range checkDiffOutputHasNoURL(out) {
			t.Errorf("runInstallCodexAssets(--diff): %v", problem)
		}
	})
}

// TestRegisteredMCPURLIgnoresEnvironment (R4): the flag is the ONLY selector.
// No environment variable moves the registered url — neither away from the
// default when the flag is absent, nor away from the flag's value when it is
// present. This row is killed by making the url composer consult any of these
// keys; it is the guard's own mutation test.
func TestRegisteredMCPURLIgnoresEnvironment(t *testing.T) {
	envKeys := []string{
		"KNOWLEDGE_MCP_PORT",
		"KNOWLEDGE_MCP_HTTP_PORT",
		"KNOWLEDGE_HTTP_PORT",
		"MCP_PORT",
	}
	t.Run("absent-flag", func(t *testing.T) {
		log, dest, _ := installClaudeHarness(t)
		for _, k := range envKeys {
			t.Setenv(k, "54321")
		}
		if err := runInstallClaudeAssets(dest); err != nil {
			t.Fatalf("runInstallClaudeAssets = %v, want nil", err)
		}
		add, err := addArgvLine(recordedLines(t, log))
		if err != nil {
			t.Fatalf("runInstallClaudeAssets: %v", err)
		}
		if want := claudeAddArgvFor(wantDefaultMCPURL); add != want {
			t.Errorf("add argv = %q, want %q (no env var may move it)", add, want)
		}
	})
	t.Run("with-flag", func(t *testing.T) {
		log, dest, _ := installCodexHarness(t)
		for _, k := range envKeys {
			t.Setenv(k, "54321")
		}
		args := append(dest, "--mcp-port", flaggedMCPPort) //nolint:gocritic // deliberate copy
		if err := runInstallCodexAssets(args); err != nil {
			t.Fatalf("runInstallCodexAssets = %v, want nil", err)
		}
		add, err := addArgvLine(recordedLines(t, log))
		if err != nil {
			t.Fatalf("runInstallCodexAssets: %v", err)
		}
		if want := codexAddArgvFor(wantFlaggedMCPURL); add != want {
			t.Errorf("add argv = %q, want %q (no env var may move it)", add, want)
		}
	})
}
