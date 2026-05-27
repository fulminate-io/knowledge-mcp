// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_types.go holds the IAMPolicy / IAMStatement / IAMPrincipal types
// and their custom JSON unmarshallers. Split out from iam_parser.go to keep
// each file under the 300-line cap.
//
// ----------------------------------------------------------------------------
// Why hand-rolled instead of aws-sdk-go-v2/service/iam/types aliases (OQ-2):
//
// The v2 PMapper plan originally proposed aliasing these types to the AWS Go
// SDK (github.com/aws/aws-sdk-go-v2/service/iam/types). On investigation the
// SDK does NOT expose parsed policy-document types at all:
//
//   - `types.PolicyDocument` does not exist.
//   - `types.Principal`       does not exist.
//   - `types.Statement` DOES exist but is a simulation-result reference
//     (StartPosition/EndPosition/SourcePolicyId) used by EvaluationResult —
//     NOT an element of a policy document. Aliasing to it would be actively
//     misleading.
//   - Every policy field in the SDK — Role.AssumeRolePolicyDocument,
//     Policy.DefaultVersionId's document, GetRolePolicyOutput.PolicyDocument —
//     is a plain `*string`. The cloud collector (cloud/aws/iam_role.go,
//     cloud/aws/iam_user.go) marshals those strings straight into node
//     metadata and node.Content. Parsing is entirely the caller's job.
//
// So the OQ-2 decision is resolved in the only way it can be: keep the
// hand-rolled IAMPolicy / IAMStatement / IAMPrincipal structs below and
// align their JSON tags with the exact wire format AWS documents publishes.
// This file is the single source of truth for how topology/ understands an
// IAM policy document.
//
// The structs below mirror the AWS policy document grammar described at:
//
//	https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_grammar.html
//
// Every field name and JSON tag matches the documented case. Statement-array-
// or-single, string-or-slice Action/Resource/NotAction/NotResource, and
// "*"-or-struct Principal variants are all handled in custom UnmarshalJSON
// implementations. Evaluation logic (AllowsAction, IsEffectiveAdmin,
// TrustPrincipals, HasCondition, ...) lives in iam_parser.go.
// ----------------------------------------------------------------------------

import (
	"encoding/json"
	"fmt"
)

// IAMPolicy is a minimal AWS IAM policy document.
//
// compat: matches AWS IAM policy document JSON shape
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_grammar.html).
// Not aliased to aws-sdk-go-v2/service/iam/types because the SDK does not
// expose parsed policy-document types — policy fields there are plain strings.
type IAMPolicy struct {
	Version    string         `json:"Version,omitempty"`
	Statements []IAMStatement `json:"Statement,omitempty"`
}

// iamPolicyJSON is the on-the-wire shape used during unmarshal. The Statement
// field can be a single statement object or an array of statement objects;
// custom UnmarshalJSON on IAMPolicy normalizes both into Statements.
type iamPolicyJSON struct {
	Version   string          `json:"Version,omitempty"`
	Statement json.RawMessage `json:"Statement,omitempty"`
}

// UnmarshalJSON normalizes the Statement field to always be a slice.
func (p *IAMPolicy) UnmarshalJSON(data []byte) error {
	var raw iamPolicyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("topology/iam_parser: unmarshal policy: %w", err)
	}
	p.Version = raw.Version
	if len(raw.Statement) == 0 {
		return nil
	}
	// Try array first; fall back to single object.
	var stmts []IAMStatement
	if err := json.Unmarshal(raw.Statement, &stmts); err == nil {
		p.Statements = stmts
		return nil
	}
	var single IAMStatement
	if err := json.Unmarshal(raw.Statement, &single); err != nil {
		return fmt.Errorf("topology/iam_parser: unmarshal statement: %w", err)
	}
	p.Statements = []IAMStatement{single}
	return nil
}

// IAMStatement is one statement in an IAM policy.
type IAMStatement struct {
	Sid          string         `json:"Sid,omitempty"`
	Effect       string         `json:"Effect,omitempty"`
	Action       []string       `json:"-"`
	NotAction    []string       `json:"-"`
	Resource     []string       `json:"-"`
	NotResource  []string       `json:"-"`
	Principal    *IAMPrincipal  `json:"Principal,omitempty"`
	NotPrincipal *IAMPrincipal  `json:"NotPrincipal,omitempty"`
	Condition    map[string]any `json:"Condition,omitempty"`
}

// iamStatementJSON is the on-the-wire shape used during unmarshal. The
// Action / Resource / NotAction / NotResource fields may be a string OR
// a slice of strings; the post-unmarshal normalization handles both.
type iamStatementJSON struct {
	Sid          string          `json:"Sid,omitempty"`
	Effect       string          `json:"Effect,omitempty"`
	Action       json.RawMessage `json:"Action,omitempty"`
	NotAction    json.RawMessage `json:"NotAction,omitempty"`
	Resource     json.RawMessage `json:"Resource,omitempty"`
	NotResource  json.RawMessage `json:"NotResource,omitempty"`
	Principal    *IAMPrincipal   `json:"Principal,omitempty"`
	NotPrincipal *IAMPrincipal   `json:"NotPrincipal,omitempty"`
	Condition    map[string]any  `json:"Condition,omitempty"`
}

// UnmarshalJSON normalizes string-or-slice fields into []string.
func (s *IAMStatement) UnmarshalJSON(data []byte) error {
	var raw iamStatementJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("topology/iam_parser: unmarshal statement: %w", err)
	}
	s.Sid = raw.Sid
	s.Effect = raw.Effect
	s.Principal = raw.Principal
	s.NotPrincipal = raw.NotPrincipal
	s.Condition = raw.Condition

	var err error
	if s.Action, err = decodeStringOrSlice(raw.Action); err != nil {
		return fmt.Errorf("topology/iam_parser: Action: %w", err)
	}
	if s.NotAction, err = decodeStringOrSlice(raw.NotAction); err != nil {
		return fmt.Errorf("topology/iam_parser: NotAction: %w", err)
	}
	if s.Resource, err = decodeStringOrSlice(raw.Resource); err != nil {
		return fmt.Errorf("topology/iam_parser: Resource: %w", err)
	}
	if s.NotResource, err = decodeStringOrSlice(raw.NotResource); err != nil {
		return fmt.Errorf("topology/iam_parser: NotResource: %w", err)
	}
	return nil
}

// decodeStringOrSlice unmarshals a json.RawMessage that may be a single
// string or a slice of strings into a normalized []string.
func decodeStringOrSlice(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var slice []string
	if err := json.Unmarshal(raw, &slice); err == nil {
		return slice, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, err
	}
	return []string{single}, nil
}

// IAMPrincipal can be the literal "*" (any principal) or a JSON object with
// AWS / Service / Federated members. Each member is itself string-or-array.
type IAMPrincipal struct {
	AWS       []string
	Service   []string
	Federated []string
	All       bool // true if Principal == "*"
}

// iamPrincipalJSON is the structured shape (when Principal is not "*").
type iamPrincipalJSON struct {
	AWS       json.RawMessage `json:"AWS,omitempty"`
	Service   json.RawMessage `json:"Service,omitempty"`
	Federated json.RawMessage `json:"Federated,omitempty"`
}

// UnmarshalJSON handles the "*"-or-struct shape variability.
func (p *IAMPrincipal) UnmarshalJSON(data []byte) error {
	// "*" → All=true, no other fields.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "*" {
			p.All = true
		}
		return nil
	}
	var raw iamPrincipalJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("topology/iam_parser: unmarshal principal: %w", err)
	}
	var err error
	if p.AWS, err = decodeStringOrSlice(raw.AWS); err != nil {
		return fmt.Errorf("topology/iam_parser: principal AWS: %w", err)
	}
	if p.Service, err = decodeStringOrSlice(raw.Service); err != nil {
		return fmt.Errorf("topology/iam_parser: principal Service: %w", err)
	}
	if p.Federated, err = decodeStringOrSlice(raw.Federated); err != nil {
		return fmt.Errorf("topology/iam_parser: principal Federated: %w", err)
	}
	return nil
}
