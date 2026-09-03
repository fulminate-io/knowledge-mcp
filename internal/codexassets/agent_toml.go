// SPDX-License-Identifier: Apache-2.0

// agent_toml.go — the ONE codex-agent-format-specific unit. emitAgentTOML
// converts a parsed Claude agent (.claude/agents/<name>.md frontmatter +
// markdown body) into a codex subagent TOML
// (~/.codex/agents/<name>.toml). All format risk is concentrated here:
// if the codex agent convention changes, only this function moves.
//
// Conversion contract (LOCKED, verified against codex-cli 0.134.0 +
// https://developers.openai.com/codex/subagents):
//   - REQUIRED keys: name, description, developer_instructions.
//   - developer_instructions = the markdown body VERBATIM. Codex
//     interprets Claude constructs ($ARGUMENTS, Agent(...),
//     mcp__knowledge__*) fine — no body rewriting.
//   - OMIT model / model_reasoning_effort / sandbox_mode / mcp_servers:
//     codex inherits them from the parent session. .claude declares
//     `model: opus`, a Claude model meaningless to codex — do NOT map
//     opus→a gpt model.
//
// go-toml/v2 (already a dep) does the marshaling. developer_instructions
// carries the ,multiline tag so the body is emitted as a TOML multiline
// basic string (""" ... """) — readable real line breaks, matching the
// codex subagents doc's own example shape — rather than one giant
// escaped single-line string. Output is deterministic across runs and
// round-trips back to the original body via toml.Unmarshal.

package codexassets

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// TranslateAgent converts a raw .claude agent markdown document (YAML
// frontmatter + body) into a codex subagent TOML document. It is the
// install-time entry point: install-codex-assets reads each
// agents/<name>.md out of the shared assets.Files embed and runs it
// through TranslateAgent to produce the on-disk <name>.toml.
//
// Returns a clear error when the frontmatter is missing or unparseable
// (parseFrontmatter ok=false) so a malformed agent file fails the
// install loudly rather than emitting a TOML with empty name/description.
func TranslateAgent(md []byte) ([]byte, error) {
	fm, body, ok := parseFrontmatter(string(md))
	if !ok {
		return nil, fmt.Errorf("agent markdown has no parseable YAML frontmatter")
	}
	return emitAgentTOML(fm, body)
}

// codexAgent is the codex subagent TOML shape. Field order in the
// struct fixes the key order in marshaled output (go-toml/v2 emits in
// struct-declaration order), which keeps emitAgentTOML deterministic so
// sync regeneration is byte-stable.
type codexAgent struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	DeveloperInstructions string `toml:"developer_instructions,multiline"`
}

// emitAgentTOML converts a parsed Claude agent frontmatter + body into a
// codex agent TOML document. The body becomes developer_instructions
// verbatim; model/sandbox/mcp_servers are intentionally omitted so the
// agent inherits them from the parent codex session.
//
// Returns the marshaled TOML bytes. Deterministic: two calls on the
// same (fm, body) produce byte-identical output.
func emitAgentTOML(fm frontmatter, body string) ([]byte, error) {
	agent := codexAgent{
		Name:                  fm.Name,
		Description:           fm.Description,
		DeveloperInstructions: body,
	}
	return toml.Marshal(agent)
}
