// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"encoding/json"
	"net/url"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// trustPolicy is a minimal representation of an AWS IAM trust policy document.
// Duplicated from topology/ since cloud/ cannot import topology/.
type trustPolicy struct {
	Statements []trustStatement `json:"Statement,omitempty"`
}

// trustPolicyJSON handles the Statement-as-single-or-array ambiguity.
type trustPolicyJSON struct {
	Statement json.RawMessage `json:"Statement,omitempty"`
}

// UnmarshalJSON normalizes Statement to always be a slice.
func (p *trustPolicy) UnmarshalJSON(data []byte) error {
	var raw trustPolicyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Statement) == 0 {
		return nil
	}
	var stmts []trustStatement
	if err := json.Unmarshal(raw.Statement, &stmts); err == nil {
		p.Statements = stmts
		return nil
	}
	var single trustStatement
	if err := json.Unmarshal(raw.Statement, &single); err != nil {
		return err
	}
	p.Statements = []trustStatement{single}
	return nil
}

// trustStatement is a single Allow/Deny statement in a trust policy.
type trustStatement struct {
	Effect    string                              `json:"Effect,omitempty"`
	Principal *trustPrincipal                     `json:"Principal,omitempty"`
	Condition map[string]map[string]stringOrSlice `json:"Condition,omitempty"`
}

// stringOrSlice handles the AWS JSON pattern where a value may be either
// a single string ("value") or an array of strings (["a","b"]).
type stringOrSlice []string

// UnmarshalJSON normalizes a single string or an array of strings into []string.
func (s *stringOrSlice) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	var slice []string
	if err := json.Unmarshal(data, &slice); err != nil {
		return err
	}
	*s = slice
	return nil
}

// trustPrincipal handles "*"-or-struct principal parsing.
type trustPrincipal struct {
	AWS       []string
	Federated []string
	All       bool
}

type trustPrincipalJSON struct {
	AWS       json.RawMessage `json:"AWS,omitempty"`
	Federated json.RawMessage `json:"Federated,omitempty"`
}

// UnmarshalJSON handles the "*"-or-struct variability.
func (p *trustPrincipal) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "*" {
			p.All = true
		}
		return nil
	}
	var raw trustPrincipalJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.AWS = decodeStringOrSliceRaw(raw.AWS)
	p.Federated = decodeStringOrSliceRaw(raw.Federated)
	return nil
}

// decodeStringOrSliceRaw decodes a json.RawMessage that may be a single
// string or a slice of strings into []string. Returns nil for empty input.
func decodeStringOrSliceRaw(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var slice []string
	if err := json.Unmarshal(raw, &slice); err == nil {
		return slice
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	return nil
}

// extractTrustPolicy parses an iam-role node's Content to get the trust policy.
func extractTrustPolicy(role *knowledgev1.Node) *trustPolicy {
	if role.Content == "" {
		return nil
	}
	var raw struct {
		AssumeRolePolicyDocument *string `json:"AssumeRolePolicyDocument,omitempty"`
	}
	if err := json.Unmarshal([]byte(role.Content), &raw); err != nil {
		return nil
	}
	if raw.AssumeRolePolicyDocument == nil || *raw.AssumeRolePolicyDocument == "" {
		return nil
	}
	encoded := *raw.AssumeRolePolicyDocument
	if p, err := parseTrustPolicyJSON([]byte(encoded)); err == nil {
		return p
	}
	if decoded, derr := url.QueryUnescape(encoded); derr == nil {
		if p, err := parseTrustPolicyJSON([]byte(decoded)); err == nil {
			return p
		}
	}
	return nil
}

// parseTrustPolicyJSON unmarshals a trust policy JSON document.
func parseTrustPolicyJSON(data []byte) (*trustPolicy, error) {
	var p trustPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// trustPolicyPrincipals returns AWS principal ARNs from Allow statements.
func trustPolicyPrincipals(p *trustPolicy) []string {
	if p == nil {
		return nil
	}
	var out []string
	for i := range p.Statements {
		s := &p.Statements[i]
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		if s.Principal == nil {
			continue
		}
		out = append(out, s.Principal.AWS...)
	}
	return out
}
