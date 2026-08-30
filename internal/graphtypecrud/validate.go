// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"fmt"
	"path/filepath"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// allowedParamTypes is the minimal closed set of collector-parameter types.
// The ticket forbids a DSL — {type, required} only — so this stays small and
// is NOT over-built.
var allowedParamTypes = map[string]struct{}{
	"string": {},
	"int":    {},
	"bool":   {},
}

// Validate enforces the record-shape invariants of a GraphTypeDef independent of
// the built-in-name collision check. It operates on the gen proto getters
// because the generated type cannot carry hand-written methods, so this is a
// package-level function using sequential wrapped-error checks.
//
// Validate does NOT check whether the name collides with a built-in GraphType —
// that registration concern needs kgtypes.allGraphTypes and lives in
// validateRegistration (same package), so Create/Update layer it on top of this.
func Validate(d *knowledgev1.GraphTypeDef) error {
	if d == nil {
		return fmt.Errorf("graphtypecrud: nil GraphTypeDef")
	}
	if strings.TrimSpace(d.GetName()) == "" {
		return fmt.Errorf("graphtypecrud: GraphTypeDef.name is required")
	}

	col := d.GetCollector()
	if col == nil {
		return fmt.Errorf("graphtypecrud: GraphTypeDef.collector is required")
	}
	bp := col.GetBinaryPath()
	if strings.TrimSpace(bp) == "" {
		return fmt.Errorf("graphtypecrud: collector.binary_path is required")
	}
	if !filepath.IsAbs(bp) {
		return fmt.Errorf("graphtypecrud: collector.binary_path %q must be absolute", bp)
	}

	if _, _, err := ParseParamTransport(col.GetParamTransport()); err != nil {
		return fmt.Errorf("graphtypecrud: collector.param_transport: %w", err)
	}

	for name, spec := range col.GetParamSchema() {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("graphtypecrud: collector.param_schema has an empty param name")
		}
		t := spec.GetType()
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("graphtypecrud: collector.param_schema[%q].type is required", name)
		}
		if _, ok := allowedParamTypes[t]; !ok {
			return fmt.Errorf("graphtypecrud: collector.param_schema[%q].type %q is not one of string/int/bool", name, t)
		}
	}

	// Cascade field lists: no empty strings, no duplicates within a list.
	if b := d.GetBehavior(); b != nil {
		if err := validateFieldList("behavior.embed_fields", b.GetEmbedFields()); err != nil {
			return err
		}
		if err := validateFieldList("behavior.summarize_fields", b.GetSummarizeFields()); err != nil {
			return err
		}
		if err := validateFieldList("behavior.bm25_fields", b.GetBm25Fields()); err != nil {
			return err
		}
	}
	for nt, ov := range d.GetNodeTypes() {
		if strings.TrimSpace(nt) == "" {
			return fmt.Errorf("graphtypecrud: node_types has an empty node-type key")
		}
		if err := validateFieldList(fmt.Sprintf("node_types[%q].embed_fields", nt), ov.GetEmbedFields()); err != nil {
			return err
		}
		if err := validateFieldList(fmt.Sprintf("node_types[%q].summarize_fields", nt), ov.GetSummarizeFields()); err != nil {
			return err
		}
		if err := validateFieldList(fmt.Sprintf("node_types[%q].bm25_fields", nt), ov.GetBm25Fields()); err != nil {
			return err
		}
	}
	return nil
}

// validateFieldList rejects empty entries and intra-list duplicates.
func validateFieldList(label string, fields []string) error {
	seen := make(map[string]struct{}, len(fields))
	for i, f := range fields {
		if strings.TrimSpace(f) == "" {
			return fmt.Errorf("graphtypecrud: %s[%d] is empty", label, i)
		}
		if _, dup := seen[f]; dup {
			return fmt.Errorf("graphtypecrud: %s contains duplicate %q", label, f)
		}
		seen[f] = struct{}{}
	}
	return nil
}

// ParseParamTransport parses a collector param_transport string. Valid forms:
//
//	"stdin"        -> kind="stdin", flagName=""
//	"flag:<name>"  -> kind="flag",  flagName="<name>" (non-empty)
//
// It is exported so T3 (the collector dispatch) reuses the same parse instead of
// re-implementing transport parsing.
func ParseParamTransport(s string) (kind, flagName string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("param_transport is required")
	}
	if s == "stdin" {
		return "stdin", "", nil
	}
	if rest, ok := strings.CutPrefix(s, "flag:"); ok {
		if strings.TrimSpace(rest) == "" {
			return "", "", fmt.Errorf("param_transport %q has an empty flag name", s)
		}
		return "flag", rest, nil
	}
	return "", "", fmt.Errorf("param_transport %q must be \"stdin\" or \"flag:<name>\"", s)
}
