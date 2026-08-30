// SPDX-License-Identifier: Apache-2.0

// assertion.go declares how a graph-shaped check EXPRESSES its assertion, and
// evaluates one against an already-materialized node/edge set.
//
// WHY THE CHANNEL IS dsl_pattern, WHICH IS FORCED RATHER THAN CHOSEN. The check
// contract carries exactly one free-form body per check: dsl_pattern, plus an
// optional check_where. Its parser validates that body AS AN AST PATTERN ONLY
// for check_type=ast_pattern, so for every other check type the body is carried
// through unvalidated and is available to the executor that owns those
// semantics. Adding a metadata key instead would be a contract change; using the
// body the contract already carries needs nothing. The assertion is therefore a
// JSON document in dsl_pattern, parsed here, strictly.
//
// ONE VOCABULARY SERVES BOTH GRAPH-SHAPED TYPES. A graph_assertion and a
// topology_threshold are the same question asked two ways — count a node's edges
// of some type in some direction, then judge the count — so they share one body,
// one parser and one evaluator, and differ only in the predicate. Every field is
// checked against the check type that may carry it: a max_degree on a
// graph_assertion, or a require on a topology_threshold, is an ERROR naming the
// field, never a silently ignored one.

package corpusscan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The two edge directions an assertion may count in.
const (
	// DirectionOut counts edges whose FROM end is the node under test.
	DirectionOut = "out"
	// DirectionIn counts edges whose TO end is the node under test.
	DirectionIn = "in"
)

// The two structural requirements a graph_assertion may state.
const (
	// RequirePresent flags a node that has NO qualifying edge.
	RequirePresent = "present"
	// RequireAbsent flags a node that has AT LEAST ONE qualifying edge.
	RequireAbsent = "absent"
)

// codeEdgeTypes is the closed set of edge types a code graph carries, used to
// refuse a typo'd edge_type BEFORE the read rather than letting it produce a
// clean-looking zero. Node types are deliberately NOT validated against a set —
// they are tree-sitter symbol kinds and the vocabulary is open — so the
// non-empty-node-set control below is what catches a typo there.
var codeEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeCalls,
	kgtypes.EdgeImports,
	kgtypes.EdgeContains,
	kgtypes.EdgeUsesType,
	kgtypes.EdgeImplements,
	kgtypes.EdgeTestCalls,
	kgtypes.EdgeLanguage,
}

// graphAssertion is the parsed body of a graph-shaped check.
type graphAssertion struct {
	// NodeType selects the candidate nodes, e.g. "function_declaration".
	NodeType string `json:"node_type"`
	// EdgeType selects which edges count toward a node's degree.
	EdgeType string `json:"edge_type"`
	// Direction is DirectionOut or DirectionIn.
	Direction string `json:"direction"`
	// Require is RequirePresent or RequireAbsent — graph_assertion only.
	Require string `json:"require,omitempty"`
	// MaxDegree flags a node whose degree EXCEEDS it — topology_threshold only.
	MaxDegree *int `json:"max_degree,omitempty"`
	// MinDegree flags a node whose degree falls BELOW it — topology_threshold only.
	MinDegree *int `json:"min_degree,omitempty"`
}

// violation is one flagged node with the measurement that flagged it.
type violation struct {
	NodeID string
	Degree int
	Reason string
}

// parseAssertion decodes and validates a graph-shaped check's body.
//
// Decoding is STRICT — an unknown field is refused rather than ignored, so a
// misspelled key can never leave an assertion quietly weaker than its author
// wrote it.
func parseAssertion(c corpus.Check) (graphAssertion, error) {
	body := strings.TrimSpace(c.Pattern)
	if body == "" {
		return graphAssertion{}, fmt.Errorf("topology/%s: check %q declares %s=%s but carries an empty %s — the assertion body is a JSON document",
			AnalyzerName, c.ID, corpus.MetaCheckType, c.Type, corpus.MetaDSLPattern)
	}
	if len(c.Where) > 0 {
		return graphAssertion{}, fmt.Errorf("topology/%s: check %q declares %s=%s and also carries %s, which only an %s check consumes — an ignored narrowing would leave the check quietly wider than written",
			AnalyzerName, c.ID, corpus.MetaCheckType, c.Type, corpus.MetaCheckWhere, corpus.CheckAstPattern)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.DisallowUnknownFields()
	var a graphAssertion
	if err := dec.Decode(&a); err != nil {
		return graphAssertion{}, fmt.Errorf("topology/%s: check %q: %s is not a well-formed assertion document: %w",
			AnalyzerName, c.ID, corpus.MetaDSLPattern, err)
	}
	if err := a.validate(c); err != nil {
		return graphAssertion{}, err
	}
	return a, nil
}

// validate enforces the vocabulary and the per-type field exclusivity.
func (a graphAssertion) validate(c corpus.Check) error {
	if strings.TrimSpace(a.NodeType) == "" {
		return fmt.Errorf("topology/%s: check %q: node_type is required — it selects the nodes the assertion is made about", AnalyzerName, c.ID)
	}
	if !isCodeEdgeType(a.EdgeType) {
		return fmt.Errorf("topology/%s: check %q: edge_type=%q is not carried by a code graph (admitted: %s)",
			AnalyzerName, c.ID, a.EdgeType, edgeTypeVocabulary())
	}
	if a.Direction != DirectionOut && a.Direction != DirectionIn {
		return fmt.Errorf("topology/%s: check %q: direction=%q is not admitted (admitted: %s, %s)",
			AnalyzerName, c.ID, a.Direction, DirectionOut, DirectionIn)
	}
	switch c.Type {
	case corpus.CheckGraphAssertion:
		return a.validateStructural(c)
	case corpus.CheckTopologyThreshold:
		return a.validateThreshold(c)
	default:
		return fmt.Errorf("topology/%s: check %q: %s=%s is not a graph-shaped check type", AnalyzerName, c.ID, corpus.MetaCheckType, c.Type)
	}
}

// validateStructural enforces the graph_assertion arm's fields.
func (a graphAssertion) validateStructural(c corpus.Check) error {
	if a.Require != RequirePresent && a.Require != RequireAbsent {
		return fmt.Errorf("topology/%s: check %q: %s=%s requires require=%s or require=%s, got %q",
			AnalyzerName, c.ID, corpus.MetaCheckType, c.Type, RequirePresent, RequireAbsent, a.Require)
	}
	if a.MaxDegree != nil || a.MinDegree != nil {
		return fmt.Errorf("topology/%s: check %q: max_degree and min_degree belong to %s, not %s — a numeric bound on a structural assertion would never be read",
			AnalyzerName, c.ID, corpus.CheckTopologyThreshold, corpus.CheckGraphAssertion)
	}
	return nil
}

// validateThreshold enforces the topology_threshold arm's fields.
func (a graphAssertion) validateThreshold(c corpus.Check) error {
	if a.Require != "" {
		return fmt.Errorf("topology/%s: check %q: require belongs to %s, not %s — state the bound with max_degree or min_degree",
			AnalyzerName, c.ID, corpus.CheckGraphAssertion, corpus.CheckTopologyThreshold)
	}
	if a.MaxDegree == nil && a.MinDegree == nil {
		return fmt.Errorf("topology/%s: check %q: %s=%s requires max_degree or min_degree — a threshold check with no threshold asserts nothing",
			AnalyzerName, c.ID, corpus.MetaCheckType, c.Type)
	}
	if a.MaxDegree != nil && *a.MaxDegree < 0 {
		return fmt.Errorf("topology/%s: check %q: max_degree=%d is negative, so every node violates it", AnalyzerName, c.ID, *a.MaxDegree)
	}
	if a.MinDegree != nil && *a.MinDegree < 0 {
		return fmt.Errorf("topology/%s: check %q: min_degree=%d is negative, so no node can violate it", AnalyzerName, c.ID, *a.MinDegree)
	}
	if a.MaxDegree != nil && a.MinDegree != nil && *a.MaxDegree < *a.MinDegree {
		return fmt.Errorf("topology/%s: check %q: max_degree=%d is below min_degree=%d, so every node violates one bound or the other",
			AnalyzerName, c.ID, *a.MaxDegree, *a.MinDegree)
	}
	return nil
}

// evaluate is the ONE evaluator, pure over an already-materialized node/edge
// set. Two producers feed it — the foundation fetchers at scan time and
// parser.Populate over a materialized fixture directory at validation time — and
// that is the whole point of the factoring: THE FIXTURE EXERCISES THE SAME
// EVALUATOR THE SCAN USES, which a hand-seeded graph fixture would not.
//
// It stays unexported deliberately. Its only callers are inside this package, so
// keeping it private is what holds this package's boundary with tools at the two
// exported symbols rather than at its whole internal shape.
func evaluate(a graphAssertion, nodes []*knowledgev1.Node, edges []*knowledgev1.Edge) []violation {
	degree := make(map[string]int, len(nodes))
	for _, e := range edges {
		if e.GetType() != a.EdgeType {
			continue
		}
		if a.Direction == DirectionOut {
			degree[e.GetFromId()]++
		} else {
			degree[e.GetToId()]++
		}
	}
	out := make([]violation, 0, len(nodes))
	for _, n := range nodes {
		if n.GetType() != a.NodeType {
			continue
		}
		d := degree[n.GetId()]
		if reason, bad := a.judge(d); bad {
			out = append(out, violation{NodeID: n.GetId(), Degree: d, Reason: reason})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// judge applies the assertion's predicate to one measured degree and, when the
// node is in violation, states WHICH bound it broke.
func (a graphAssertion) judge(d int) (string, bool) {
	switch {
	case a.Require == RequirePresent && d == 0:
		return fmt.Sprintf("has no %s edge %s, and the check requires at least one", a.EdgeType, a.Direction), true
	case a.Require == RequireAbsent && d > 0:
		return fmt.Sprintf("has %d %s edge(s) %s, and the check requires none", d, a.EdgeType, a.Direction), true
	case a.MaxDegree != nil && d > *a.MaxDegree:
		return fmt.Sprintf("has %d %s edge(s) %s, above the max_degree of %d", d, a.EdgeType, a.Direction, *a.MaxDegree), true
	case a.MinDegree != nil && d < *a.MinDegree:
		return fmt.Sprintf("has %d %s edge(s) %s, below the min_degree of %d", d, a.EdgeType, a.Direction, *a.MinDegree), true
	default:
		return "", false
	}
}

// isCodeEdgeType reports whether the spelling is one a code graph carries.
func isCodeEdgeType(t string) bool {
	for _, e := range codeEdgeTypes {
		if t == string(e) {
			return true
		}
	}
	return false
}

// edgeTypeVocabulary renders the admitted edge types for an error message,
// derived from the constants so it cannot enumerate a set the parser rejects.
func edgeTypeVocabulary() string {
	parts := make([]string, 0, len(codeEdgeTypes))
	for _, e := range codeEdgeTypes {
		parts = append(parts, string(e))
	}
	return strings.Join(parts, ", ")
}
