// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// wellFormed returns a minimal valid GraphTypeDef that each negative case
// mutates into a rejection.
func wellFormed() *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name: "jira",
		Collector: &knowledgev1.CollectorSpec{
			BinaryPath:     "/usr/local/bin/jira-collector",
			ParamTransport: "stdin",
			ParamSchema: map[string]*knowledgev1.ParamSpec{
				"project": {Type: "string", Required: true},
			},
		},
		Behavior: &knowledgev1.BehaviorDefaults{
			EmbedFields: []string{"description"},
		},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(d *knowledgev1.GraphTypeDef)
		wantErr bool
	}{
		{"well-formed", func(d *knowledgev1.GraphTypeDef) {}, false},
		{"nil collector ok via wellFormed", func(d *knowledgev1.GraphTypeDef) {}, false},
		{"empty name", func(d *knowledgev1.GraphTypeDef) { d.Name = "" }, true},
		{"whitespace name", func(d *knowledgev1.GraphTypeDef) { d.Name = "   " }, true},
		{"nil collector", func(d *knowledgev1.GraphTypeDef) { d.Collector = nil }, true},
		{"empty binary_path", func(d *knowledgev1.GraphTypeDef) { d.Collector.BinaryPath = "" }, true},
		{"relative binary_path", func(d *knowledgev1.GraphTypeDef) { d.Collector.BinaryPath = "bin/collector" }, true},
		{"dot-relative binary_path", func(d *knowledgev1.GraphTypeDef) { d.Collector.BinaryPath = "./collector" }, true},
		{"bad transport", func(d *knowledgev1.GraphTypeDef) { d.Collector.ParamTransport = "pipe" }, true},
		{"empty transport", func(d *knowledgev1.GraphTypeDef) { d.Collector.ParamTransport = "" }, true},
		{"flag transport ok", func(d *knowledgev1.GraphTypeDef) { d.Collector.ParamTransport = "flag:--cfg" }, false},
		{"flag transport empty name", func(d *knowledgev1.GraphTypeDef) { d.Collector.ParamTransport = "flag:" }, true},
		{"empty param type", func(d *knowledgev1.GraphTypeDef) {
			d.Collector.ParamSchema["project"] = &knowledgev1.ParamSpec{Type: ""}
		}, true},
		{"unknown param type", func(d *knowledgev1.GraphTypeDef) {
			d.Collector.ParamSchema["project"] = &knowledgev1.ParamSpec{Type: "float"}
		}, true},
		{"empty embed field", func(d *knowledgev1.GraphTypeDef) {
			d.Behavior.EmbedFields = []string{"a", ""}
		}, true},
		{"dup embed field", func(d *knowledgev1.GraphTypeDef) {
			d.Behavior.EmbedFields = []string{"a", "a"}
		}, true},
		{"override dup bm25", func(d *knowledgev1.GraphTypeDef) {
			d.NodeTypes = map[string]*knowledgev1.NodeTypeOverride{
				"issue": {Bm25Fields: []string{"x", "x"}},
			}
		}, true},
		{"empty node-type key", func(d *knowledgev1.GraphTypeDef) {
			d.NodeTypes = map[string]*knowledgev1.NodeTypeOverride{"": {}}
		}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := wellFormed()
			tc.mutate(d)
			err := Validate(d)
			if tc.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}

	if err := Validate(nil); err == nil {
		t.Error("Validate(nil) should error")
	}
}

func TestParseParamTransport(t *testing.T) {
	tests := []struct {
		in       string
		wantKind string
		wantFlag string
		wantErr  bool
	}{
		{"stdin", "stdin", "", false},
		{"flag:--config", "flag", "--config", false},
		{"flag:c", "flag", "c", false},
		{"flag:", "", "", true},
		{"", "", "", true},
		{"pipe", "", "", true},
		{"  stdin  ", "stdin", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			kind, flag, err := ParseParamTransport(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseParamTransport(%q) = nil err, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseParamTransport(%q) error: %v", tc.in, err)
			}
			if kind != tc.wantKind || flag != tc.wantFlag {
				t.Errorf("ParseParamTransport(%q) = (%q,%q), want (%q,%q)", tc.in, kind, flag, tc.wantKind, tc.wantFlag)
			}
		})
	}
}
