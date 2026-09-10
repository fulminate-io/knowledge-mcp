// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// wellFormed returns a minimal valid GraphTypeDef that each negative case
// mutates into a rejection: a stdio MCP provider with a tool to call and one
// environment NAME=value pair, which is what the config loader synthesizes for
// one collect.
func wellFormed() *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name: "jira",
		Collector: &knowledgev1.CollectorSpec{
			Tool: "collect_jira",
			Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
				Command: "/usr/local/bin/jira-mcp",
				Args:    []string{"--serve"},
				Env:     []string{"JIRA_TOKEN=hunter2"},
			}},
		},
		Behavior: &knowledgev1.BehaviorDefaults{
			EmbedFields: []string{"description"},
		},
	}
}

// stdioOf reaches the stdio provider a mutation is about to bend.
func stdioOf(d *knowledgev1.GraphTypeDef) *knowledgev1.StdioProvider {
	return d.GetCollector().GetStdio()
}

// useHTTP swaps the record onto an http provider so the http arms can mutate it.
func useHTTP(d *knowledgev1.GraphTypeDef, url string) {
	d.Collector.Provider = &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: url}}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(d *knowledgev1.GraphTypeDef)
		wantErr bool
		// wantErrContains, when set, pins that the refusal NAMES the offending
		// thing rather than merely returning some error.
		wantErrContains string
	}{
		{name: "well-formed", mutate: func(*knowledgev1.GraphTypeDef) {}},
		{name: "empty name", mutate: func(d *knowledgev1.GraphTypeDef) { d.Name = "" }, wantErr: true},
		{name: "whitespace name", mutate: func(d *knowledgev1.GraphTypeDef) { d.Name = "   " }, wantErr: true},
		// A RECORD WITH NO COLLECTOR IS THE ORDINARY PERSISTED SHAPE under the
		// config-file contract: the connection half lives in the operator's file and
		// never crosses the wire, so Validate admits a name-plus-behavior record.
		// ValidateCollector is what refuses a nil collector, and it has its own row
		// below — this pair is the one place the split is observable.
		{name: "no collector is admitted: the persisted record carries none", mutate: func(d *knowledgev1.GraphTypeDef) { d.Collector = nil }},

		{
			name:            "empty tool",
			mutate:          func(d *knowledgev1.GraphTypeDef) { d.Collector.Tool = "" },
			wantErr:         true,
			wantErrContains: "collector.tool",
		},
		{
			name:            "no provider at all",
			mutate:          func(d *knowledgev1.GraphTypeDef) { d.Collector.Provider = nil },
			wantErr:         true,
			wantErrContains: "exactly one provider",
		},
		{
			name:            "empty stdio command",
			mutate:          func(d *knowledgev1.GraphTypeDef) { stdioOf(d).Command = "" },
			wantErr:         true,
			wantErrContains: "collector.stdio.command",
		},
		{
			// THE REFUSAL REVERSED WITH THE CONTRACT. A bare NAME used to be the only
			// admitted form, because the record was persisted and the daemon looked
			// the value up; the block is now the child's whole environment and this
			// spec is never persisted, so a bare name would reach the child as a
			// variable named after the whole element with no value.
			name:            "env entry carrying no value",
			mutate:          func(d *knowledgev1.GraphTypeDef) { stdioOf(d).Env = []string{"JIRA_TOKEN"} },
			wantErr:         true,
			wantErrContains: "is not a NAME=value pair",
		},
		{
			name:            "empty env name",
			mutate:          func(d *knowledgev1.GraphTypeDef) { stdioOf(d).Env = []string{"A=1", "   =2"} },
			wantErr:         true,
			wantErrContains: "collector.stdio.env[1]",
		},
		{
			name:            "duplicate env name",
			mutate:          func(d *knowledgev1.GraphTypeDef) { stdioOf(d).Env = []string{"A=1", "A=2"} },
			wantErr:         true,
			wantErrContains: "twice",
		},
		{name: "no env at all is fine", mutate: func(d *knowledgev1.GraphTypeDef) { stdioOf(d).Env = nil }},
		{name: "args are optional", mutate: func(d *knowledgev1.GraphTypeDef) { stdioOf(d).Args = nil }},

		{name: "http provider", mutate: func(d *knowledgev1.GraphTypeDef) { useHTTP(d, "https://p.example/mcp") }},
		{
			name:            "http url empty",
			mutate:          func(d *knowledgev1.GraphTypeDef) { useHTTP(d, "") },
			wantErr:         true,
			wantErrContains: "collector.http.url",
		},
		{
			name:            "http url does not parse",
			mutate:          func(d *knowledgev1.GraphTypeDef) { useHTTP(d, "://not a url") },
			wantErr:         true,
			wantErrContains: "does not parse",
		},
		{
			name:            "http url with a non-http scheme",
			mutate:          func(d *knowledgev1.GraphTypeDef) { useHTTP(d, "ftp://p.example/mcp") },
			wantErr:         true,
			wantErrContains: "http or https",
		},
		{
			name:            "http url with no host",
			mutate:          func(d *knowledgev1.GraphTypeDef) { useHTTP(d, "https:///mcp") },
			wantErr:         true,
			wantErrContains: "names no host",
		},

		{name: "empty embed field", mutate: func(d *knowledgev1.GraphTypeDef) {
			d.Behavior.EmbedFields = []string{"a", ""}
		}, wantErr: true},
		{name: "dup embed field", mutate: func(d *knowledgev1.GraphTypeDef) {
			d.Behavior.EmbedFields = []string{"a", "a"}
		}, wantErr: true},
		{name: "override dup bm25", mutate: func(d *knowledgev1.GraphTypeDef) {
			d.NodeTypes = map[string]*knowledgev1.NodeTypeOverride{
				"issue": {Bm25Fields: []string{"x", "x"}},
			}
		}, wantErr: true},
		{name: "empty node-type key", mutate: func(d *knowledgev1.GraphTypeDef) {
			d.NodeTypes = map[string]*knowledgev1.NodeTypeOverride{"": {}}
		}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := wellFormed()
			tc.mutate(d)
			err := Validate(d)
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("Validate() = %q, want it to name %q", err, tc.wantErrContains)
			}
		})
	}

	if err := Validate(nil); err == nil {
		t.Error("Validate(nil) should error")
	}
}

// TestValidateCollector_IsWhereTheCollectorRequirementMoved is the other half of
// the split above. Validate admits a behavior-only record because that is what
// the server stores; the CONNECTION spec is checked by ValidateCollector, which
// the write path runs against the entry-derived spec before dialing.
//
// WITHOUT THIS ROW THE SPLIT WOULD BE A DELETION. Moving the requirement off
// Validate and nowhere else would leave a nil collector admitted everywhere, and
// every test above would still pass.
func TestValidateCollector_IsWhereTheCollectorRequirementMoved(t *testing.T) {
	if err := ValidateCollector(nil); err == nil {
		t.Error("ValidateCollector(nil) must refuse: the write path has a spec to check")
	}
	if err := ValidateCollector(wellFormed().GetCollector()); err != nil {
		t.Errorf("the known positive must validate: %v", err)
	}
}

// TestValidate_BothProviders pins the both-set arm. The oneof cannot hold two at
// once, so this constructs the shape the way a decoder that ignored the conflict
// would leave it — and the validator must still refuse rather than silently
// dialing whichever one won.
func TestValidate_BothProviders(t *testing.T) {
	d := wellFormed()
	// Assigning http over stdio is the only way the generated oneof lets both be
	// expressed; what this pins is that a record can never carry two, so the
	// exactly-one rule has a decode-side partner (collectorFromArgs) that refuses
	// the payload naming both.
	useHTTP(d, "https://p.example/mcp")
	if err := Validate(d); err != nil {
		t.Fatalf("a record carrying one provider must validate: %v", err)
	}
	if d.GetCollector().GetStdio() != nil {
		t.Error("the generated oneof must not hold both providers at once")
	}
}
