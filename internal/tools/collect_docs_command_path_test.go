// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_docs_command_path_test.go — every worked collector entry in the guides
// gives `command` an ABSOLUTE PATH, and the guide says why.
//
// THE DEFECT THIS EXISTS FOR IS NOT A TYPO, it is a difference between two
// processes that an operator has no reason to suspect. The daemon resolves a
// stdio entry's command once with exec.LookPath, in the process serving the
// collect (externalcollector/mcphost.go, stdioTransport). LookPath reads the
// PATH of THAT process. A daemon started by a service manager does not inherit a
// login shell's environment — under launchd its PATH is
// /usr/bin:/bin:/usr/sbin:/sbin — so a command installed under a user prefix
// never resolves at collect time.
//
// WHAT MAKES IT WORSE THAN A PLAIN MISCONFIGURATION IS THAT REGISTRATION
// SUCCEEDS. `knowledge collector add` dials the provider from the operator's own
// shell, with the operator's PATH, so a bare name passes the contract check at
// registration and fails at the first collect. The guide showing bare names is
// therefore teaching a shape that is verified by the one step that cannot see
// the problem.
//
// AND THE OBVIOUS REMEDY DOES NOT WORK: putting PATH in the entry's env block
// changes the CHILD's environment, which mcphost.go assigns AFTER the lookup has
// already happened. It cannot change how the provider's own name was resolved.
//
// SCOPE — WORKED VALUES, NOT PROSE. A bare command name is correct English in a
// sentence about a program ("run ticket-mcp"); it is a worked configuration
// value in a FENCED BLOCK or in a `knowledge collector add` invocation, and both
// are checked. Those are what a reader copies. Prose naming a program is
// untouched, and the guide's own `command -v ticket-mcp` line — which is how a
// reader FINDS the absolute path — must stay bare.
//
// THE FENCE SCAN IS SPELLING-INDEPENDENT, and it is deliberately not tied to the
// fence's info string. A review measured the earlier version's boundary: it read
// ```json and ```jsonc and was SILENT on a yaml fence (`command: ticket-mcp`), a
// toml fence (`command = "ticket-mcp"`) and an unlabelled ``` fence carrying a
// json body. The guide uses none of those today, so that was a drift boundary
// rather than a live miss — but keying a gate on the fence LABEL is the same
// mistake as keying it on one separator, which this sweep already made once and
// paid for. Every fence is now walked whatever its label, and a `command` key is
// recognized in all three spellings.
func TestGuideEntries_GiveCommandAnAbsolutePath(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	require.NotEmpty(t, pages, "the census must reach the guide pages")

	// KNOWN POSITIVE, run before the assertion: the fence scanner really finds
	// commands. A scanner that matched nothing — a changed fence marker, a
	// renamed field — would make every absence below vacuous, and this is the
	// class of silent pass the whole file exists to prevent.
	totalCommands := 0
	for _, body := range pages {
		totalCommands += len(commandValuesInFences(body))
	}
	require.Positive(t, totalCommands,
		"the fence scanner found no %q value in any guide page; it is not reading the fences, so the assertion below proves nothing", "command")

	for page, body := range pages {
		for _, cmd := range commandValuesInFences(body) {
			assert.Truef(t, strings.HasPrefix(cmd, "/"),
				"%s: a worked entry gives command %q, a bare name. The daemon resolves it with LookPath against ITS OWN "+
					"PATH, which under a service manager is not the shell's — so this entry registers cleanly and fails at "+
					"the first collect. Show an absolute path.", page, cmd)
		}
	}

	// THE OTHER PLACE A READER COPIES A COMMAND FROM is the `collector add`
	// invocation, where it follows the `--` terminator. It is the SAME value
	// landing in the same entry field by a different route, so a gate covering
	// only the JSON fences would leave half the page teaching the bad shape —
	// and the CLI example is the half a reader reaches first.
	addCommands := 0
	for page, body := range pages {
		for _, cmd := range commandsAfterAddTerminator(body) {
			addCommands++
			assert.Truef(t, strings.HasPrefix(cmd, "/"),
				"%s: a worked `knowledge collector add` runs %q after the terminator, a bare name. That value is written "+
					"into the entry verbatim and resolved later against the DAEMON's PATH, so the dial succeeds in your "+
					"shell and the first collect fails. Show an absolute path.", page, cmd)
		}
	}
	// KNOWN POSITIVE for the second scanner, on the same terms as the first.
	require.Positive(t, addCommands,
		"the terminator scanner found no `collector add ... -- <command>` invocation in any guide page; it is not reading them")
}

// commandsAfterAddTerminator returns the command each worked `knowledge
// collector add` invocation runs after its `--` terminator.
//
// IT READS THE WHOLE PAGE, not only fenced blocks: the guide shows this shape
// inline in prose as well as in bash fences, and both are copied. A line
// carrying no `collector add` is skipped, so ordinary prose containing a double
// dash is not a candidate.
//
// THE INVOCATION WRAPS, which is why the scan carries a continuation. The worked
// example puts its flags on the first line and `<name> -- <command>` on the
// last, joined by a trailing backslash, so a line-at-a-time scan that did not
// follow the continuation would find no terminator at all and report a vacuous
// zero — which is what the known-positive above exists to catch.
func commandsAfterAddTerminator(body string) []string {
	var out []string
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "collector add") {
			continue
		}
		// Join the continuation lines so the terminator is reachable.
		joined := strings.TrimSpace(line)
		for j := i; j < len(lines)-1 && strings.HasSuffix(strings.TrimSpace(lines[j]), "\\"); j++ {
			joined = strings.TrimSuffix(strings.TrimSpace(joined), "\\") + " " + strings.TrimSpace(lines[j+1])
		}
		if cmd, ok := firstWordAfterTerminator(joined); ok {
			out = append(out, cmd)
		}
	}
	return out
}

// firstWordAfterTerminator returns the first token following a standalone `--`,
// skipping the USAGE SYNOPSIS.
//
// A SYNOPSIS IS NOT A WORKED INVOCATION. The guide's CLI section opens with
// `... <name> -- <command> [args...]`, which names the SHAPE of the argument
// rather than a value anyone copies literally; the gate flagged it on its first
// run. An angle-bracket token is the guide's own spelling for a placeholder
// throughout, and no real command can begin with one, so skipping it costs no
// coverage — a bare `npx` or `ticket-mcp` is still a hit, which the planted
// positives drive.
func firstWordAfterTerminator(line string) (string, bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		if f != "--" || i+1 >= len(fields) {
			continue
		}
		tok := strings.Trim(fields[i+1], "`")
		if strings.HasPrefix(tok, "<") {
			return "", false
		}
		return tok, true
	}
	return "", false
}

// TestGuideStatesTheDaemonPathRule is the positive half, and it is the half that
// cannot be satisfied by deleting something. The assertion above is an absence:
// a page with every worked entry removed would pass it. These require the guide
// to still TEACH the rule, so the reason survives alongside the corrected
// examples.
func TestGuideStatesTheDaemonPathRule(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the custom-collector guide")

	// Each row fits on one line of the wrapped guide, for the reason the sibling
	// docs assertions record: a matcher spanning the wrap fails on a reflow that
	// changed nothing a reader sees.
	for _, want := range []string{
		"resolved against the DAEMON's PATH",
		"/usr/bin:/bin:/usr/sbin:/sbin",
		"proves nothing about this",
		"is applied",
	} {
		assert.Containsf(t, page, want,
			"the guide must state the daemon-PATH rule: an absolute path, why the registration dial cannot see the problem, "+
				"and why putting PATH in the env block does not help. Missing: %q", want)
	}
}

// commandValuesInFences returns every `command` VALUE found inside any fenced
// block in body, in any of the three spellings a fenced configuration uses.
//
// EVERY FENCE IS WALKED, WHATEVER ITS LABEL. An opening fence is any line
// starting with three backticks; the next such line closes it. Keying on the
// info string is what left the earlier version silent on yaml, toml and
// unlabelled fences, and a gate keyed on a label is a gate the next author walks
// around by typing a different label.
//
// IT IS DELIBERATELY A SCANNER RATHER THAN A PARSE. The guide's fences are JSONC
// and carry // comments, so encoding/json refuses several of them; a parse that
// skipped the unparseable ones would silently drop exactly the richest examples.
// And there is no one parser for three formats. Reading lines sees all of them.
//
// THE BASH FENCES ARE NOT A FALSE-POSITIVE SOURCE, and the key matcher is what
// makes that true rather than luck: it requires `command` to be the FIRST token
// on the line, optionally after a yaml list dash, followed by `:` or `=`. A shell
// line like `command -v ticket-mcp` has a flag next, not a separator, so it is
// not a candidate — which matters because that exact line is how the guide tells
// a reader to FIND the absolute path.
func commandValuesInFences(body string) []string {
	var out []string
	inFence := false
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		if v, ok := jsonStringField(trimmed, "command"); ok {
			out = append(out, v)
			continue
		}
		if v, ok := keyedCommandValue(trimmed); ok {
			out = append(out, v)
		}
	}
	return out
}

// keyedCommandValue reads the yaml and toml spellings: a line whose first token
// is `command` (optionally after a yaml list dash) followed by `:` or `=`, then
// a value that may be bare, double-quoted or single-quoted.
//
// A TRAILING COMMENT IS STRIPPED for the bare form, because both formats admit
// one and a value carrying `# a note` is the value plus noise rather than a
// different value.
func keyedCommandValue(line string) (string, bool) {
	rest := strings.TrimPrefix(line, "- ")
	rest, ok := strings.CutPrefix(strings.TrimSpace(rest), "command")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	switch {
	case strings.HasPrefix(rest, ":"):
		rest = rest[1:]
	case strings.HasPrefix(rest, "="):
		rest = rest[1:]
	default:
		// `command` followed by anything else is not a key: a shell invocation,
		// a prose word inside a fence, a longer identifier such as command_path.
		return "", false
	}
	rest = strings.TrimSpace(rest)
	for _, q := range []string{`"`, `'`} {
		if after, found := strings.CutPrefix(rest, q); found {
			val, closed := strings.CutSuffix(strings.TrimSpace(after), q)
			if !closed {
				if idx := strings.Index(val, q); idx >= 0 {
					val = val[:idx]
				}
			}
			return val, true
		}
	}
	// Bare value: take the first field and drop a trailing comment.
	if idx := strings.Index(rest, "#"); idx >= 0 {
		rest = rest[:idx]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// jsonStringField pulls the string value of `"<field>": "<value>"` out of one
// line, returning ok=false when the line does not carry that field as a string.
func jsonStringField(line, field string) (string, bool) {
	key := `"` + field + `"`
	_, after, ok := strings.Cut(line, key)
	if !ok {
		return "", false
	}
	rest := after
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	rest = rest[colon+1:]
	open := strings.Index(rest, `"`)
	if open < 0 {
		return "", false
	}
	rest = rest[open+1:]
	value, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return "", false
	}
	return value, true
}
