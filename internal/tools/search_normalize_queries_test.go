// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"
)

// TestNormalizeQueriesToQuery covers the queries→query fold for the non-code
// search arms: a `queries`-only payload must populate `query` (the knowledge
// client engine + server dispatch read only `query`), an explicit `query` wins,
// and every other arg is preserved.
func TestNormalizeQueriesToQuery(t *testing.T) {
	get := func(raw json.RawMessage, key string) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		var s string
		_ = json.Unmarshal(m[key], &s)
		return s
	}

	cases := []struct {
		name      string
		in        string
		wantQuery string
		wantGraph string // preserved-field check
	}{
		{"queries-only single", `{"queries":["alpha"],"graph":"knowledge"}`, "alpha", "knowledge"},
		{"queries-only multi", `{"queries":["alpha","beta"],"graph":"knowledge"}`, "alpha beta", "knowledge"},
		{"explicit query wins", `{"query":"keep","queries":["ignored"],"graph":"practice"}`, "keep", "practice"},
		{"query-only untouched", `{"query":"solo","graph":"code"}`, "solo", "code"},
		{"neither untouched", `{"graph":"knowledge","limit":5}`, "", "knowledge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeQueriesToQuery(json.RawMessage(tc.in))
			if got := get(out, "query"); got != tc.wantQuery {
				t.Errorf("query = %q, want %q", got, tc.wantQuery)
			}
			if got := get(out, "graph"); got != tc.wantGraph {
				t.Errorf("graph (preserved field) = %q, want %q", got, tc.wantGraph)
			}
		})
	}
}
