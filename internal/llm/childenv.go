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
// process so it never starts the dream Runner: it short-circuits both
// RunWorkerSubcommand and wireWorkerRuntime (see bootstrap.ParseFlags /
// daemon wiring). It exists so a knowledge process can be run purely to
// serve/exercise the graph without spinning its own background worker
// runtime — e.g. the bench harness (cmd/client-bench) sets it on the
// knowledge process it launches.
const NoWorkerRuntimeFlag = "--no-worker-runtime"
