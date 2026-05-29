// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"os"
	"strings"
)

// ChildEnv builds the environment for a subprocess launched by a CLI-backed
// LLM provider: it takes os.Environ(), drops every variable named in
// dropKeys, then appends each "KEY=VALUE" string in adds.
//
// The dropKeys filtering exists so the claude-cli / codex-cli providers can
// strip the API-key env var (ANTHROPIC_API_KEY / OPENAI_API_KEY) before
// exec — those providers exist to authenticate via the user's local CLI
// login (subscription / ChatGPT account), and a stray API key inherited
// from a parent shell or a launchd plist would otherwise silently route the
// call through paid-API billing instead. (Real incident, May 2026.)
//
// adds are appended verbatim after the filtered base; the std library's
// exec package keeps the last assignment for a duplicated key, so an
// entry in adds also overrides any same-named var that survived the drop.
func ChildEnv(dropKeys []string, adds ...string) []string {
	base := os.Environ()
	drop := make(map[string]struct{}, len(dropKeys))
	for _, k := range dropKeys {
		drop[k] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(adds))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, skip := drop[name]; skip {
			continue
		}
		out = append(out, kv)
	}
	return append(out, adds...)
}

// NoWorkerRuntimeFlag is the cmd/knowledge flag that demotes a knowledge
// process to a pure MCP stdio server: it short-circuits both
// RunWorkerSubcommand and wireWorkerRuntime so the process never starts the
// dream Runner. It is the recursion guard for every CLI-backed provider that
// spawns a child knowledge process as an MCP server.
const NoWorkerRuntimeFlag = "--no-worker-runtime"

// ChildKnowledgeArgs returns the argv tail a CLI-backed provider passes to a
// child knowledge process it launches as a stdio MCP server. It is the SOLE
// owner of the dream-worker fork-bomb guard: it inherits the parent's flags
// (os.Args[1:] — so the child dials the SAME knowledge-server: --port,
// --graph-storage, --root, --log-level, …) and guarantees NoWorkerRuntimeFlag
// is present (deduped).
//
// Why the guard is load-bearing: a dream worker drives tool-use by having its
// LLM CLI (claude-cli / codex-cli) spawn this child as an MCP server. If the
// child ALSO wired its dream Runner, a worker triggered there would spawn
// another CLI → another child → … an unbounded fork bomb. NoWorkerRuntimeFlag
// makes the child a pure receiver of tool calls, terminating the recursion at
// depth 1. Both providers MUST route through here — duplicating the guard per
// provider is exactly how a safety invariant drifts out of sync.
func ChildKnowledgeArgs(parentArgs []string) []string {
	out := make([]string, 0, len(parentArgs))
	if len(parentArgs) > 0 {
		out = append(out, parentArgs[1:]...)
	}
	for _, a := range out {
		if a == NoWorkerRuntimeFlag || a == "-no-worker-runtime" {
			return out
		}
	}
	return append(out, NoWorkerRuntimeFlag)
}
