// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// describe.go — the DESCRIBE TOOL: the second tool every custom collector must
// serve, its checked-in output schema, and the declaration document it returns.
//
// WHAT DESCRIBE IS FOR. A registration entry says what a collector IS — which
// node and edge types it emits, which fields its text is composed from, which
// environment variables it reads, which foreign-graph context it needs — and
// every one of those facts is known to the collector and to nobody else. Before
// this tool an operator transcribed them from a README into their entry, and an
// installer carried a hand-written table per collector. Now the collector
// answers, `collector add` fills the entry from the answer, and the installer's
// tables are derived from it.
//
// THE NAME IS FIXED AND IS NEVER DECLARED. The client must know a tool's name
// before it can call any tool on a provider, so the one thing a declaration
// cannot supply is the name of the tool that supplies it. The entry's `tool`
// field continues to name the COLLECT tool only.
//
// THE TWO LLM AXES ARE A SUGGESTION AND NEVER A SETTING. Summarizing and
// embedding are LLM spend on the operator's account, so they come from the
// operator's flags; what the collector suggests is PRINTED by `collector add`
// and never written. The rest of the declaration is written as declared.

// DescribeToolName is the name of the required second tool. It is a constant on
// both sides of the wire — here and in the collector framework — and the two
// must agree.
const DescribeToolName = "describe"

//go:embed contract/collector_describe.schema.json
var describeContractJSON []byte

// DescribeContractJSON exposes the checked-in describe contract schema verbatim,
// so the docs generator and the tests read the same bytes the checks run against
// rather than a transcription of them.
func DescribeContractJSON() []byte { return slices.Clone(describeContractJSON) }

// The four environment classes. The class decides what an installer WRITES,
// not merely what the variable means, so the vocabulary is closed and a
// declaration naming a fourth class is refused rather than admitted and
// interpreted.
//
//   - path: a literal from the installing shell. Without it a provider's
//     file-based credential chain resolves nothing.
//   - selector: a literal when set; the key is OMITTED ENTIRELY when unset,
//     because an empty selector selects the thing named by the empty string.
//   - secret: NOTHING is written, in any state — not the value and not a
//     reference to it, since a reference is expanded by the process serving the
//     collect and arrives present-and-empty.
//   - not-carried: NOTHING is written either, and for a different reason: the
//     collector READS the name, and an installed entry deliberately does not
//     declare it. It is a machine fact (a trust-root path), a platform the
//     installer does not target, or a non-default deployment the operator
//     configures by hand. DECLARING IT IS THE POINT: an operator whose variable
//     is absent from the entry can tell a decision from an oversight, and the
//     installer's own table is derived by dropping exactly these rows.
const (
	EnvClassPath       = "path"
	EnvClassSelector   = "selector"
	EnvClassSecret     = "secret"
	EnvClassNotCarried = "not-carried"
)

// envClasses is the closed vocabulary, sorted so a refusal lists it stably.
var envClasses = []string{EnvClassNotCarried, EnvClassPath, EnvClassSecret, EnvClassSelector}

// envNamePattern is the shape an environment variable name must have. It is the
// installer's own rule, checked here so a declaration that could never be
// installed is refused at the add that would have written it rather than at the
// install that consumes it (install.sh refuses a row whose name carries a
// character outside this set).
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Declaration is the document the describe tool returns: everything about a
// collector that its registration entry records.
//
// EVERY FIELD MAPS ONTO AN ENTRY FIELD OR ONTO THE ADD OUTPUT. Nothing here is
// advisory except the two behavior booleans, which are printed rather than
// applied; a field the entry has no home for would be a fact the operator
// cannot see, which is what the README-transcription state already was.
type Declaration struct {
	// Behavior is the graph-level behavior this collector suggests. It is a
	// pointer so a document that omitted it is refused by name rather than read
	// as a declaration of every default.
	Behavior *DeclaredBehavior `json:"behavior"`
	// NodeTypeOverrides is the per-node-type half of the cascade, keyed by node
	// type. The key set is the collector's own vocabulary.
	NodeTypeOverrides map[string]DeclaredNodeTypeOverride `json:"node_type_overrides,omitempty"`
	// NodeTypes is the NODE TYPE VOCABULARY: every node type this collector
	// emits. Once it reaches the registration record, a collect carrying a type
	// outside it is refused at ingest by name.
	NodeTypes []string `json:"node_types"`
	// EdgeTypes is the EDGE TYPE VOCABULARY, on the same closed terms.
	EdgeTypes []string `json:"edge_types"`
	// Environment is the variables this collector reads, each with its class. It
	// is a declaration of NAMES: a value is refused.
	Environment []DeclaredEnv `json:"environment"`
	// Context is the foreign-graph context this collector needs, in the entry's
	// own shape. It carries no `reason` key — the entry's decoder refuses an
	// unknown field by name, so a rendered reason would make the entry
	// unloadable.
	Context ContextDeclaration `json:"context,omitempty"`
}

// DeclaredBehavior is the behavior half of a declaration. The three booleans are
// POINTERS on the same rule the entry's own block follows: presence is what
// distinguishes a declaration from a silence, and the schema requires all three
// so a collector cannot be silent about them by accident.
type DeclaredBehavior struct {
	// Summarizable is a SUGGESTION. The written entry takes this from the
	// operator's flag.
	Summarizable *bool `json:"summarizable"`
	// Embeddable is a SUGGESTION, on the same terms.
	Embeddable *bool `json:"embeddable"`
	// Syncable costs no LLM call and is written as declared.
	Syncable        *bool    `json:"syncable"`
	EmbedFields     []string `json:"embed_fields,omitempty"`
	SummarizeFields []string `json:"summarize_fields,omitempty"`
	Bm25Fields      []string `json:"bm25_fields,omitempty"`
}

// DeclaredNodeTypeOverride is one node type's override of the graph-level
// behavior. An unset key means inherit, which is the server's own rule.
type DeclaredNodeTypeOverride struct {
	Summarizable    *bool    `json:"summarizable,omitempty"`
	Embeddable      *bool    `json:"embeddable,omitempty"`
	EmbedFields     []string `json:"embed_fields,omitempty"`
	SummarizeFields []string `json:"summarize_fields,omitempty"`
	Bm25Fields      []string `json:"bm25_fields,omitempty"`
}

// DeclaredEnv is one environment variable this collector reads.
//
// THERE IS NO VALUE FIELD AND THERE MUST NOT BE ONE. The declaration crosses a
// process boundary from a provider the operator installed; a value on it would
// be a credential traveling on a path nothing audits, and the strict decode
// refuses one by name today.
type DeclaredEnv struct {
	Name  string `json:"name"`
	Class string `json:"class"`
	// Description is what the collector reads the variable for, shown to an
	// operator by an installer. It is optional.
	Description string `json:"description,omitempty"`
	// EmptySensitive declares that this collector tells the name PRESENT AND
	// EMPTY apart from ABSENT: it refuses such a value, branches on the name's
	// presence, or hands it to a dependency that does either.
	//
	// ABSENT MEANS FALSE. A collector written before the property existed, and a
	// collector that discriminates on nothing, both decode to false and produce
	// the entry they always produced.
	//
	// IT CHANGES NOTHING THIS CLIENT WRITES. The class decides what reaches an
	// entry, and a marked name of any class is written exactly as an unmarked one
	// of that class is. What the mark governs is a DOCUMENT: a worked entry
	// showing `${NAME:-}` hands a child the name present and empty, which is a
	// broken collect for a marked name and inert for the rest, so the shipped
	// documentation gate refuses that form for marked names only. The mark is
	// legal on every class including not-carried, which is the one class no
	// installed entry carries and which can still appear in a document.
	EmptySensitive bool `json:"empty_sensitive,omitempty"`
}

// CheckDescribeToolSchema is the describe half of the registration gate: the
// describe tool must advertise an output schema, and that schema must satisfy
// the checked-in describe contract.
//
// THERE IS NO INPUT ARM, and that is a property of the tool rather than a
// relaxation. describe takes no arguments — there is nothing about a collect for
// it to be parameterized by — so there is no input contract to compare, and a
// provider is free to advertise whatever empty-object input schema its SDK
// generates.
func CheckDescribeToolSchema(tool string, advertisedOutput any) error {
	if advertisedOutput == nil {
		return fmt.Errorf(
			"custom collector: tool %q advertises NO output schema; the collector contract requires the describe tool to declare "+
				"its behavior defaults, node and edge type vocabularies and environment names (see contract/collector_describe.schema.json)", tool)
	}
	return checkAgainstContract(tool, "describe output", describeContractJSON, advertisedOutput)
}

// DecodeDeclaration validates a describe call's structuredContent against the
// CONTRACT describe schema and decodes it into a Declaration.
//
// TWO GATES ASKING DIFFERENT QUESTIONS, exactly as DecodeResult does on the
// collect side. The schema validation asks whether the payload is a conforming
// declaration at all; the strict decode asks whether every field it carries is
// one this client knows how to write into an entry. A key the Declaration does
// not name is an ERROR rather than a silent drop, because a fact a collector
// declared and the entry silently omitted is the transcription failure this tool
// exists to end.
func DecodeDeclaration(tool string, structured any) (*Declaration, error) {
	if structured == nil {
		return nil, fmt.Errorf(
			"custom collector: tool %q returned no structuredContent; the contract requires a structured declaration matching its advertised output schema", tool)
	}
	raw, err := json.Marshal(structured)
	if err != nil {
		return nil, fmt.Errorf("custom collector: tool %q result is not JSON: %w", tool, err)
	}
	if err := validateDeclarationPayload(tool, raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Declaration
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf(
			"custom collector: tool %q declaration carries a field this collector contract does not define: %w", tool, err)
	}
	if err := d.Validate(tool); err != nil {
		return nil, err
	}
	return &d, nil
}

// validateDeclarationPayload validates raw declaration JSON against the
// checked-in describe contract schema.
func validateDeclarationPayload(tool string, raw []byte) error {
	contract, err := parseSchema(describeContractJSON)
	if err != nil {
		return fmt.Errorf("custom collector: the checked-in describe contract schema is unreadable: %w", err)
	}
	resolved, err := contract.Resolve(nil)
	if err != nil {
		return fmt.Errorf("custom collector: the checked-in describe contract schema does not resolve: %w", err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return fmt.Errorf("custom collector: tool %q declaration is not JSON: %w", tool, err)
	}
	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf(
			"custom collector: tool %q declaration does not satisfy the collector contract's describe schema: %w", tool, err)
	}
	return nil
}

// Validate refuses a declaration whose CLOSED vocabulary is wrong, naming the
// value that is wrong with it.
//
// THE SCHEMA CANNOT ASK EVERY QUESTION THIS DOES. It pins the class vocabulary
// and the required keys, and it cannot pin that a name is a variable name the
// installer can write, that a name appears once, or that the declared foreign
// context is one this client can supply. Each of those is a declaration that
// would install cleanly and fail later, which is the shape "bad input always
// errors" exists to stop.
func (d *Declaration) Validate(tool string) error {
	seen := make(map[string]struct{}, len(d.Environment))
	for i, e := range d.Environment {
		if err := ValidateDeclaredEnv(e); err != nil {
			return fmt.Errorf("custom collector: tool %q declares environment[%d]: %w", tool, i, err)
		}
		if _, dup := seen[e.Name]; dup {
			return fmt.Errorf(
				"custom collector: tool %q declares the environment name %q twice; one name has one class", tool, e.Name)
		}
		seen[e.Name] = struct{}{}
	}
	for _, nt := range d.NodeTypes {
		if strings.TrimSpace(nt) == "" {
			return fmt.Errorf("custom collector: tool %q declares an empty node type; a vocabulary entry names a type", tool)
		}
	}
	for _, et := range d.EdgeTypes {
		if strings.TrimSpace(et) == "" {
			return fmt.Errorf("custom collector: tool %q declares an empty edge type; a vocabulary entry names a type", tool)
		}
	}
	// THE CONTEXT DECLARATION IS HELD TO THE ENTRY'S OWN STANDARD, here rather
	// than only at the write: a declaration this client cannot satisfy would be
	// written into the entry and refused at the operator's next collect, naming a
	// file they did not write.
	if err := d.Context.Validate(); err != nil {
		return fmt.Errorf("custom collector: tool %q declares foreign-graph context this client cannot honor: %w", tool, err)
	}
	return nil
}

// verifiedDescribeTool resolves the REQUIRED describe tool in a provider's
// listing and asserts its output schema against the checked-in describe
// contract.
//
// THE REFUSAL IS THE ONE A COLLECTOR AUTHOR READS FIRST, so it says three
// things rather than one: that the tool is required and missing, what the tool
// must return, and which checked-in file describes it. Every provider written
// before this tool existed is refused here — that is the decided behavior of a
// REQUIRED tool rather than an oversight — and an author whose refusal named
// only a missing tool would have nothing to act on.
func verifiedDescribeTool(ctx context.Context, session *mcp.ClientSession) (*mcp.Tool, error) {
	tool, err := findTool(ctx, session, DescribeToolName)
	if err != nil {
		return nil, fmt.Errorf(
			"custom collector: this provider does not serve the REQUIRED %q tool, so its registration entry cannot be "+
				"filled from it: %w. Add a %q tool that takes no arguments and returns the collector's declaration — its "+
				"suggested behavior defaults and field lists, its per-node-type overrides, the node and edge types it "+
				"emits, the environment variables it reads with their class, and any foreign-graph context it needs. The "+
				"shape is contract/collector_describe.schema.json; a collector built on the Go framework serves it by "+
				"implementing Describe()",
			DescribeToolName, err, DescribeToolName)
	}
	if err := CheckDescribeToolSchema(tool.Name, tool.OutputSchema); err != nil {
		return nil, err
	}
	return tool, nil
}

// callDescribe calls the describe tool on an open session and decodes its
// declaration. It is called ONCE, at `knowledge collector add`, where the answer
// is written into the entry.
//
// EVERY REFUSAL WRITES NOTHING, on the same rule the collect path states: the
// caller writes the entry only from what this returns, so a provider error
// result, a non-conforming declaration or a field this contract does not define
// all return an error and no entry at all.
func callDescribe(ctx context.Context, session *mcp.ClientSession) (*Declaration, error) {
	tool, err := verifiedDescribeTool(ctx, session)
	if err != nil {
		return nil, err
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{}})
	if err != nil {
		return nil, fmt.Errorf("custom collector: calling tool %q failed: %w", tool.Name, err)
	}
	if res.IsError {
		return nil, fmt.Errorf("custom collector: tool %q reported an error result: %s", tool.Name, toolErrorText(res))
	}
	decl, err := DecodeDeclaration(tool.Name, res.StructuredContent)
	if err != nil {
		return nil, err
	}
	// The provider's own advertised schema is the second assertion, and it is a
	// different question from the contract's: the contract says what a
	// declaration must always be, this says whether the provider kept its own
	// word.
	if err := ValidateAgainstAdvertised(tool.Name, "declaration", tool.OutputSchema, res.StructuredContent); err != nil {
		return nil, err
	}
	return decl, nil
}

// ValidateDeclaredEnv refuses one environment declaration whose name an
// installer could not write or whose class is outside the closed four.
//
// IT IS EXPORTED FOR THE CONFIG LOADER, which holds the same rows on an entry
// that was hand-edited rather than filled by an add. One implementation reached
// from both routes is what keeps the two from drifting into different answers
// about the same row.
func ValidateDeclaredEnv(e DeclaredEnv) error {
	if !envNamePattern.MatchString(e.Name) {
		return fmt.Errorf(
			"the environment name %q is not an environment variable name; a name matches [A-Za-z_][A-Za-z0-9_]* "+
				"and an installer refuses anything else", e.Name)
	}
	if !slices.Contains(envClasses, e.Class) {
		return fmt.Errorf(
			"the environment name %q carries the class %q; the classes are %s — and the class is what decides "+
				"whether a value is ever written into a config file", e.Name, e.Class, strings.Join(envClasses, ", "))
	}
	return nil
}

// DeclaresNodeVocabulary reports whether this declaration names any node type.
// It is the add path's own question: a collector that declares an empty
// vocabulary has declared that it emits no nodes, which is a real declaration
// and is refused at ingest for every node it then sends.
func (d *Declaration) DeclaresNodeVocabulary() bool { return d != nil && d.NodeTypes != nil }

// EnvNamesOfClass returns the declared names of one class, sorted, so a caller
// rendering an installer table produces one byte-identical table per unchanged
// declaration.
func (d *Declaration) EnvNamesOfClass(class string) []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.Environment))
	for _, e := range d.Environment {
		if e.Class == class {
			out = append(out, e.Name)
		}
	}
	slices.Sort(out)
	return out
}
