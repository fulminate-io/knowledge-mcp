// SPDX-License-Identifier: Apache-2.0

package codexassets

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// probeSkillsRoot is the fixed skills root these emitter tests translate
// against. Any non-empty path does; a literal keeps the expectations
// independent of whatever root the machine running the test would resolve.
const probeSkillsRoot = "/probe/skills-root"

// emitAgentTOML on planner.md produces TOML
// round-tripping via toml.Unmarshal into a map with name='planner',
// non-empty description, developer_instructions containing the body,
// and NO model key.
func TestEmitAgentTOML_PlannerRoundTrip(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "planner.md"))
	if err != nil {
		t.Fatalf("read planner.md: %v", err)
	}
	fm, body, ok := parseFrontmatter(string(data))
	if !ok {
		t.Fatal("parseFrontmatter ok=false")
	}
	out, err := emitAgentTOML(fm, body, probeSkillsRoot)
	if err != nil {
		t.Fatalf("emitAgentTOML: %v", err)
	}
	var m map[string]any
	if err := toml.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal emitted TOML: %v\n---\n%s", err, out)
	}
	if m["name"] != "planner" {
		t.Errorf("name = %v, want planner", m["name"])
	}
	if desc, _ := m["description"].(string); desc == "" {
		t.Error("description empty, want non-empty")
	}
	di, _ := m["developer_instructions"].(string)
	if di == "" {
		t.Fatal("developer_instructions empty")
	}
	if !strings.Contains(di, "<precedence>") {
		t.Error("developer_instructions does not contain the body's <precedence> block")
	}
	if !strings.Contains(di, strings.TrimSpace(firstLine(strings.TrimLeft(body, "\n")))) {
		t.Error("developer_instructions does not contain the body verbatim")
	}
	if _, present := m["model"]; present {
		t.Error("model key present, want omitted (codex inherits parent default)")
	}
	if _, present := m["sandbox_mode"]; present {
		t.Error("sandbox_mode key present, want omitted")
	}
	if _, present := m["mcp_servers"]; present {
		t.Error("mcp_servers key present, want omitted")
	}
}

// Two emitAgentTOML calls on identical (fm,body)
// produce byte-identical output — deterministic emit so sync
// regeneration is byte-stable.
func TestEmitAgentTOML_Deterministic(t *testing.T) {
	fm := frontmatter{
		Name:        "demo",
		Description: "A demo agent for determinism testing.",
		Model:       "opus",
	}
	body := "Line one.\nLine two with \"quotes\" and a $ARGUMENTS token.\n"
	a, err := emitAgentTOML(fm, body, probeSkillsRoot)
	if err != nil {
		t.Fatalf("first emit: %v", err)
	}
	b, err := emitAgentTOML(fm, body, probeSkillsRoot)
	if err != nil {
		t.Fatalf("second emit: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("non-deterministic emit:\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
}

// developer_instructions must be emitted as a TOML multiline basic string
// (""" ... """) — readable line breaks per the codex subagents doc shape —
// and must round-trip EXACTLY, including a body's leading newline (TOML trims
// the first newline after """, so the encoder has to compensate).
//
// The expected value is the rendered resolvedPathsPreamble + a blank line +
// the body. It is built from the CONST BY NAME rather than re-typed: a second
// literal copy of the preamble text would drift silently from the emitter's.
func TestEmitAgentTOML_MultilineFormatRoundTrip(t *testing.T) {
	fm := frontmatter{Name: "demo", Description: "A demo agent."}
	// Body starts AND ends with a newline (as a real markdown body does
	// after frontmatter) and carries a backslash regex + quotes.
	body := "\n<precedence>\nUse search, not grep -rn '\\.Method('.\nSay \"hi\".\n"
	out, err := emitAgentTOML(fm, body, probeSkillsRoot)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !strings.Contains(string(out), `developer_instructions = """`) {
		t.Errorf("developer_instructions not emitted as a multiline basic string:\n%s", out)
	}
	var m map[string]any
	if err := toml.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	want := fmt.Sprintf(resolvedPathsPreamble, probeSkillsRoot) + "\n\n" + body
	if di, _ := m["developer_instructions"].(string); di != want {
		t.Errorf("developer_instructions did not round-trip exactly:\n want %q\n got  %q", want, di)
	}
}

// No real secret/key/token literal in agent_toml.go.
// Self-check the source for accidental embedded credentials. Any
// mcp_servers example must use a bearer_token_env_var placeholder, not a
// literal token.
func TestEmitAgentTOML_NoSecrets(t *testing.T) {
	src, err := os.ReadFile("agent_toml.go")
	if err != nil {
		t.Fatalf("read agent_toml.go: %v", err)
	}
	lower := strings.ToLower(string(src))
	for _, needle := range []string{"sk-ant-", "sk-proj-", "bearer ", "api_key=\"", "secret=\""} {
		if strings.Contains(lower, needle) {
			t.Errorf("agent_toml.go contains a possible secret literal: %q", needle)
		}
	}
}
