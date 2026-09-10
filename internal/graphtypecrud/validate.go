// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"fmt"
	"net/url"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// Validate enforces the record-shape invariants of a GraphTypeDef independent of
// the built-in-name collision check. It operates on the gen proto getters
// because the generated type cannot carry hand-written methods, so this is a
// package-level function using sequential wrapped-error checks.
//
// Validate does NOT check whether the name collides with a built-in GraphType —
// that is ValidateName, which validateRegistration layers on top of this.
//
// It also does NOT dial the provider: whether the named tool exists and
// advertises contract-satisfying schemas is a live question the MCP host
// answers, at `knowledge collector add` and at collect alike. This function is
// the pure record-shape half, so it stays runnable with no network.
//
// A RECORD WITH NO COLLECTOR IS ADMISSIBLE, AND IS NOW THE ORDINARY CASE. Under
// the config-file contract the persisted record carries the family NAME and its
// BEHAVIOR only: the connection half lives in the operator's config file and
// never crosses the wire, because no server file reads it and because the env
// block now carries VALUES, which must never be stored in a graph-resident node.
// The collector requirement moved to ValidateCollector, which the write path
// runs against the FILE-derived spec before dialing. A record that does carry a
// collector — a legacy one being re-written — is still validated whole.
func Validate(d *knowledgev1.GraphTypeDef) error {
	if d == nil {
		return fmt.Errorf("graphtypecrud: nil GraphTypeDef")
	}
	if strings.TrimSpace(d.GetName()) == "" {
		return fmt.Errorf("graphtypecrud: GraphTypeDef.name is required")
	}

	if col := d.GetCollector(); col != nil {
		if err := ValidateCollector(col); err != nil {
			return err
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

// ValidateCollector enforces the MCP collector spec: a tool to call and EXACTLY
// ONE provider transport, each with its own shape rules.
//
// IT IS EXPORTED BECAUSE THE SPEC NO LONGER LIVES IN THE PERSISTED RECORD. The
// spec is synthesized from a config-file entry for one collect, so the caller
// that has one to check is the write path, not this package's own upsert.
func ValidateCollector(col *knowledgev1.CollectorSpec) error {
	if col == nil {
		return fmt.Errorf("graphtypecrud: collector is required")
	}
	if strings.TrimSpace(col.GetTool()) == "" {
		return fmt.Errorf("graphtypecrud: collector.tool is required — it names the MCP tool to call on the provider")
	}
	stdio, http := col.GetStdio(), col.GetHttp()
	switch {
	case stdio == nil && http == nil:
		return fmt.Errorf("graphtypecrud: collector must name exactly one provider — supply either stdio{command,args,env} or http{url}")
	case stdio != nil && http != nil:
		return fmt.Errorf("graphtypecrud: collector names both a stdio and an http provider — exactly one is allowed")
	case stdio != nil:
		return validateStdio(stdio)
	default:
		return validateHTTP(http)
	}
}

// validateStdio enforces the stdio provider's shape.
//
// THE ENV LIST CARRIES NAME=value PAIRS, AND THAT REVERSED WITH THE CONTRACT.
// It used to carry NAMES ONLY, because the record was persisted and the daemon
// looked each name up in its own environment — so a pair carrying a value was
// refused as a secret in a stored record. Under the config-file contract this
// spec is SYNTHESIZED FROM THE OPERATOR'S ENTRY FOR ONE COLLECT and is never
// persisted, the block IS the child's whole environment, and os/exec wants
// NAME=value. So a bare name is now the refusal: it would reach the child as a
// variable named after the whole element with no value, which is not what
// anybody who wrote one meant.
func validateStdio(s *knowledgev1.StdioProvider) error {
	if strings.TrimSpace(s.GetCommand()) == "" {
		return fmt.Errorf("graphtypecrud: collector.stdio.command is required")
	}
	seen := make(map[string]struct{}, len(s.GetEnv()))
	for i, pair := range s.GetEnv() {
		name, _, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf(
				"graphtypecrud: collector.stdio.env[%d] %q is not a NAME=value pair — the env block IS the child's whole environment, so every entry carries the value the provider will see", i, pair)
		}
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("graphtypecrud: collector.stdio.env[%d] has an empty variable name", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("graphtypecrud: collector.stdio.env sets %q twice", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// validateHTTP enforces the http provider's shape. There is no credential field
// to validate — the operator's headers ride beside the record, and the daemon
// composes none of its own — so this is the endpoint alone.
func validateHTTP(h *knowledgev1.HttpProvider) error {
	raw := strings.TrimSpace(h.GetUrl())
	if raw == "" {
		return fmt.Errorf("graphtypecrud: collector.http.url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("graphtypecrud: collector.http.url %q does not parse: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("graphtypecrud: collector.http.url %q must be an http or https URL", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("graphtypecrud: collector.http.url %q names no host", raw)
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
