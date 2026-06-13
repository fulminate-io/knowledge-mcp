// SPDX-License-Identifier: Apache-2.0

package tools

import "testing"

// TestHiveToolDef_AgentOpsOnly pins the agent-facing hive schema to EXACTLY the
// five agent ops (register/send/claim/ack/fail) and asserts the daemon ops
// (renew/evict) are ABSENT. The daemon ops ride the same RPC + HiveOp enum but
// must never be advertised to the LLM. This test goes red if a future edit leaks
// a daemon op into the agent schema or drops an agent op.
func TestHiveToolDef_AgentOpsOnly(t *testing.T) {
	def := HiveToolDef()
	if def.Name != "hive" {
		t.Fatalf("hive tool name = %q, want %q", def.Name, "hive")
	}
	op, ok := def.InputSchema.Properties["op"]
	if !ok {
		t.Fatal("hive schema must declare an op property")
	}

	wantOps := map[string]bool{"register": true, "send": true, "claim": true, "ack": true, "fail": true}
	if len(op.Enum) != len(wantOps) {
		t.Fatalf("op enum = %v (len %d), want exactly the %d agent ops", op.Enum, len(op.Enum), len(wantOps))
	}
	seen := map[string]bool{}
	for _, e := range op.Enum {
		if e == "renew" || e == "evict" {
			t.Errorf("daemon op %q must NOT appear in the agent-facing hive schema", e)
		}
		if !wantOps[e] {
			t.Errorf("unexpected op %q in hive schema", e)
		}
		seen[e] = true
	}
	for want := range wantOps {
		if !seen[want] {
			t.Errorf("agent op %q missing from hive schema", want)
		}
	}

	// op is required (the tool is op-dispatched).
	requiredOp := false
	for _, r := range def.InputSchema.Required {
		if r == "op" {
			requiredOp = true
		}
	}
	if !requiredOp {
		t.Error("op must be a required param on the op-dispatched hive tool")
	}
}
