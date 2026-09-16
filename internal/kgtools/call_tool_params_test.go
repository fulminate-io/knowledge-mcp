// SPDX-License-Identifier: Apache-2.0

package kgtools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestCallToolParams_NamesNoSessionArgument is THE REMOVAL PIN, written by
// REFLECTION over the struct type rather than against a fixture string, so it
// states the CONTRACT — "no tool argument names a session" — rather than one
// spelling of it. A field added back under any Go name with a session-naming
// json tag reds this.
//
// Why the contract matters: a session identity is something the harness passes,
// never something a caller names for itself. A decodable `session_id` parameter
// is an alternate carrier, and an alternate carrier is exactly what the
// no-fallback rule exists to prevent.
func TestCallToolParams_NamesNoSessionArgument(t *testing.T) {
	typ := reflect.TypeFor[CallToolParams]()
	if _, ok := typ.FieldByName("SessionID"); ok {
		t.Error("CallToolParams carries a SessionID field; a tool argument must never name a session")
	}
	for f := range typ.Fields() {
		tag := f.Tag.Get("json")
		if tag == "session_id" || tag == "session_id,omitempty" {
			t.Errorf("CallToolParams field %q decodes the json key session_id", f.Name)
		}
	}
}

// TestCallToolParams_IgnoresASessionIDArgument is the pin's sibling at the
// WIRE: a tools/call whose params carry a session_id decodes without it
// reaching anything. The removed field is not a carrier, and a params blob
// naming one is simply an unknown key.
func TestCallToolParams_IgnoresASessionIDArgument(t *testing.T) {
	var p CallToolParams
	body := []byte(`{"name":"search","arguments":{},"session_id":"smuggled-session"}`)
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Name != "search" {
		t.Fatalf("name = %q, want search", p.Name)
	}
	// Re-marshal: nothing the struct models can carry the smuggled value.
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, unwanted := range []string{"smuggled-session", "session_id"} {
		if strings.Contains(string(out), unwanted) {
			t.Errorf("a session_id argument survived the decode/encode round trip (%q): %s", unwanted, out)
		}
	}
}

// TestCallToolMeta_DecodesBothHarnessCarriers pins the `_meta` widening against
// the exact shapes each harness sends (Claude Code 2.1.272 and Codex 0.154.0,
// both reproduced by probe). The keys are undocumented by their vendors, so the
// absent-key arm is a first-class row: it must decode to zero values rather
// than error.
func TestCallToolMeta_DecodesBothHarnessCarriers(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		var p CallToolParams
		body := []byte(`{"name":"search","_meta":{"claudecode/toolUseId":"toolu_01ABC","progressToken":3}}`)
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.Meta == nil || p.Meta.ClaudeToolUseID != "toolu_01ABC" {
			t.Errorf("meta = %+v, want claudecode/toolUseId toolu_01ABC", p.Meta)
		}
	})

	t.Run("codex", func(t *testing.T) {
		var p CallToolParams
		body := []byte(`{"name":"search","_meta":{"callId":"exec-3e8c","itemId":"i","progressToken":0,` +
			`"threadId":"01a0a596","x-codex-turn-metadata":{"session_id":"01a0a596-04b3","turn_id":"t","model":"m"}}}`)
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.Meta == nil || p.Meta.CallID != "exec-3e8c" {
			t.Errorf("meta = %+v, want callId exec-3e8c", p.Meta)
		}
		if p.Meta.CodexTurn == nil || p.Meta.CodexTurn.SessionID != "01a0a596-04b3" {
			t.Errorf("turn metadata = %+v, want session_id 01a0a596-04b3", p.Meta.CodexTurn)
		}
	})

	t.Run("no _meta at all", func(t *testing.T) {
		var p CallToolParams
		if err := json.Unmarshal([]byte(`{"name":"search","arguments":{}}`), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.Meta != nil {
			t.Errorf("meta = %+v, want nil — an absent carrier is an expected shape", p.Meta)
		}
	})
}
