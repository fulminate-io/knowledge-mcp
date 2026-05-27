// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// rerank_pipeline.go owns the rerank pipeline DSL: a closed set of three ops
// (filter / score / limit) that pre/post-shape search results around the
// existing voyage rerank step. The DSL is closed by design — adding ops
// requires editing the ParsePipeline switch, not registering at init time.
//
// File organization mirrors reranker_render.go / reranker_voyage.go: same
// package, type definitions and per-op machinery in one file. If the
// Predicate machinery grows, the natural split point is a sibling
// rerank_pipeline_predicate.go relocating Predicate methods only.
//
// Wire format (concrete-op `op` discriminator carried as a struct field so
// encoding/json's default marshaler emits {"op":"filter",...} without any
// custom MarshalJSON apparatus):
//
//   {
//     "pre":  [ {"op":"filter","where":{...},"action":"drop"} ],
//     "post": [ {"op":"score","where":{...},"mode":"multiply","value":0.8},
//               {"op":"limit","n":20} ]
//   }

// Pipeline is a sequence of ops applied around the voyage rerank step.
// Pre runs before voyage; Post runs after. LimitOp is rejected in Pre by
// Pipeline.Validate (the rule is positional, not per-op).
//
// Pipeline.UnmarshalJSON delegates to ParsePipeline so encoding/json's
// default decoder validates on receipt. There is no exported MarshalJSON;
// the default struct marshaler over Pre/Post + the concrete-op `Op string`
// discriminator field is sufficient.
type Pipeline struct {
	Pre  []Op `json:"pre,omitempty"`
	Post []Op `json:"post,omitempty"`
}

// Op is the closed-set rerank pipeline operator interface. Implementations:
// FilterOp, ScoreOp, LimitOp. The interface has exactly three methods —
// Name (op discriminator string for error messages and dispatch),
// Apply (transform a hydrated result slice given the live query string),
// Validate (eager parse-time checks; called from Pipeline.Validate which
// is itself called from ParsePipeline).
//
// No registry. No init-time wiring. New ops require a ParsePipeline switch
// case and a concrete type — by design.
type Op interface {
	Name() string
	Apply(query string, in []engine.SearchResult) ([]engine.SearchResult, error)
	Validate() error
}

// FilterOp drops or keeps results based on a Predicate. Action is "drop"
// (skip matches) or "keep" (only retain matches).
type FilterOp struct {
	Op     string    `json:"op"`
	Where  Predicate `json:"where"`
	Action string    `json:"action"`
}

// Name returns the op discriminator. Always "filter".
func (o *FilterOp) Name() string { return "filter" }

// Validate enforces the Action enum and recursively validates the Where
// predicate (depth, mutual exclusion, regex compile).
func (o *FilterOp) Validate() error {
	switch o.Action {
	case "drop", "keep":
	default:
		return fmt.Errorf("filter op: action must be \"drop\" or \"keep\", got %q", o.Action)
	}
	if err := o.Where.validate(0); err != nil {
		return fmt.Errorf("filter op: %w", err)
	}
	return nil
}

// Apply evaluates the Where predicate against each result and either drops
// matches (action="drop") or keeps only matches (action="keep"). Empty
// input passes through unchanged. The output is a freshly allocated slice;
// the input is never mutated. No re-sort — filter preserves order.
func (o *FilterOp) Apply(query string, in []engine.SearchResult) ([]engine.SearchResult, error) {
	if len(in) == 0 {
		return in, nil
	}
	out := make([]engine.SearchResult, 0, len(in))
	keepOnMatch := o.Action == "keep"
	for i := range in {
		matched := o.Where.Eval(query, in[i].Node)
		if matched == keepOnMatch {
			out = append(out, in[i])
		}
	}
	return out, nil
}

// ScoreOp adjusts the Score field of matching results. Mode is "multiply"
// (Score *= Value), "add" (Score += Value), or "set" (Score = Value).
// After applying, the result slice is re-sorted by Score descending using
// sort.SliceStable to preserve original relative order on ties.
type ScoreOp struct {
	Op    string    `json:"op"`
	Where Predicate `json:"where"`
	Mode  string    `json:"mode"`
	Value float64   `json:"value"`
}

// Name returns the op discriminator. Always "score".
func (o *ScoreOp) Name() string { return "score" }

// Validate enforces the Mode enum and recursively validates the Where
// predicate.
func (o *ScoreOp) Validate() error {
	switch o.Mode {
	case "multiply", "add", "set":
	default:
		return fmt.Errorf("score op: mode must be \"multiply\", \"add\", or \"set\", got %q", o.Mode)
	}
	if err := o.Where.validate(0); err != nil {
		return fmt.Errorf("score op: %w", err)
	}
	return nil
}

// Apply mutates the Score of each matching result per Mode (multiply / add
// / set), then re-sorts the result slice by Score descending using
// sort.SliceStable so original relative order is preserved on ties. Empty
// input passes through unchanged. The input slice is never mutated — Apply
// allocates a fresh slice and copies before mutating.
func (o *ScoreOp) Apply(query string, in []engine.SearchResult) ([]engine.SearchResult, error) {
	if len(in) == 0 {
		return in, nil
	}
	out := make([]engine.SearchResult, len(in))
	copy(out, in)
	for i := range out {
		if !o.Where.Eval(query, out[i].Node) {
			continue
		}
		switch o.Mode {
		case "multiply":
			out[i].Score *= o.Value
		case "add":
			out[i].Score += o.Value
		case "set":
			out[i].Score = o.Value
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out, nil
}

// LimitOp truncates the result slice to the first N elements. Rejected in
// Pre by Pipeline.Validate (positional rule, not enforced here).
type LimitOp struct {
	Op string `json:"op"`
	N  int    `json:"n"`
}

// Name returns the op discriminator. Always "limit".
func (o *LimitOp) Name() string { return "limit" }

// Validate enforces N > 0. The Pre/Post position rule is enforced at the
// Pipeline level, not here.
func (o *LimitOp) Validate() error {
	if o.N <= 0 {
		return fmt.Errorf("limit op: n must be > 0, got %d", o.N)
	}
	return nil
}

// Apply truncates the result slice to the first N elements. Empty input
// passes through; input shorter than N also passes through. Pipeline.Validate
// already rejects LimitOp in Pre, so the caller-position invariant is
// guaranteed by the time Apply runs.
func (o *LimitOp) Apply(query string, in []engine.SearchResult) ([]engine.SearchResult, error) {
	if len(in) == 0 {
		return in, nil
	}
	if len(in) <= o.N {
		return in, nil
	}
	return in[:o.N], nil
}

// Predicate is the recursive boolean DSL used by FilterOp/ScoreOp. EXACTLY
// ONE of (Field+Match+Value) — the leaf form — or (Any/All/Not) — the
// boolean composition form — is set per Predicate. Validate enforces this
// mutual exclusion plus a max nesting depth of 3.
//
// Field is one of the closed set: file_path, symbol_name, type, summary,
// description, keywords, signature, status, content — or `metadata.<key>`
// (prefix match; resolved via Node.Value at Eval time).
//
// Match is one of: regex, prefix, suffix, contains, equals, in,
// tokens_match. Phase 1 Step 3 implements Eval.
//
// Negate inverts the result of the leaf match or the boolean composition
// (applied once, at the end, as XOR).
//
// `compiled` caches the *regexp.Regexp parsed eagerly inside Validate when
// Match == "regex". Eager compile is mandatory: parse-time error
// surfacing + concurrent-Apply safety (no lazy-cache races).
type Predicate struct {
	Field  string          `json:"field,omitempty"`
	Match  string          `json:"match,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
	Negate bool            `json:"negate,omitempty"`

	Any []Predicate `json:"any,omitempty"`
	All []Predicate `json:"all,omitempty"`
	Not *Predicate  `json:"not,omitempty"`

	// compiled is the eagerly-compiled regex when Match == "regex".
	// Populated by validate(), read by Eval. Unexported and JSON-skipped
	// so it does not appear in wire output. Eager population sidesteps
	// any lazy-compile concurrency hazard.
	compiled *regexp.Regexp `json:"-"`
}

// validate is the recursive Predicate validator. depth counts boolean
// composition levels (any/all/not) and is bounded at 3. Step 1 wires the
// mutual-exclusion check + depth check + leaf-match enum membership.
// Eager regex compile lives here too — Step 3 fills in the actual cache
// population once the operator vocabulary is wired through Eval. For Step
// 1 we already populate compiled inside the regex branch so callers
// (Pipeline.Validate via per-op Validate) get the eager-compile guarantee
// from day one.
func (p *Predicate) validate(depth int) error {
	if depth > 3 {
		return errors.New("predicate nesting depth exceeds 3")
	}

	leafSet := p.Field != "" || p.Match != "" || len(p.Value) > 0
	boolSet := len(p.Any) > 0 || len(p.All) > 0 || p.Not != nil
	if leafSet == boolSet {
		// Both set, or neither set — both are errors.
		if leafSet && boolSet {
			return errors.New("predicate: cannot mix leaf form (field/match/value) with boolean form (any/all/not)")
		}
		return errors.New("predicate: empty — must set either (field+match+value) or (any/all/not)")
	}

	if leafSet {
		return p.validateLeaf()
	}

	// Boolean composition: validate each child at depth+1.
	if len(p.Any) > 0 {
		for i := range p.Any {
			if err := p.Any[i].validate(depth + 1); err != nil {
				return fmt.Errorf("any[%d]: %w", i, err)
			}
		}
	}
	if len(p.All) > 0 {
		for i := range p.All {
			if err := p.All[i].validate(depth + 1); err != nil {
				return fmt.Errorf("all[%d]: %w", i, err)
			}
		}
	}
	if p.Not != nil {
		if err := p.Not.validate(depth + 1); err != nil {
			return fmt.Errorf("not: %w", err)
		}
	}
	return nil
}

// validateLeaf checks the leaf-form fields (Field+Match+Value) and, for
// regex matches, eagerly compiles the pattern into p.compiled so per-Apply
// runs hit a cached *regexp.Regexp. $query interpolation in regex values
// is rejected here — allowing it would couple validation to the live query
// and reintroduce per-Apply regex compile.
func (p *Predicate) validateLeaf() error {
	if p.Field == "" {
		return errors.New("predicate leaf: field required")
	}
	if !validPredicateField(p.Field) {
		return fmt.Errorf("predicate leaf: unknown field %q", p.Field)
	}
	if p.Match == "" {
		return errors.New("predicate leaf: match required")
	}
	if !validPredicateMatch(p.Match) {
		return fmt.Errorf("predicate leaf: unknown match %q", p.Match)
	}
	if len(p.Value) == 0 {
		return errors.New("predicate leaf: value required")
	}
	if p.Match != "regex" {
		return nil
	}
	var pat string
	if err := json.Unmarshal(p.Value, &pat); err != nil {
		return fmt.Errorf("predicate regex: value must be a JSON string: %w", err)
	}
	if pat == "$query" {
		return errors.New("predicate regex: $query interpolation not allowed in regex value")
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return fmt.Errorf("predicate regex: compile %q: %w", pat, err)
	}
	p.compiled = re
	return nil
}

// validPredicateField returns true for the closed set of leaf field names
// plus the metadata.<key> prefix form. Field names are lowercase by
// convention (matching the JSON wire shape of Node).
func validPredicateField(f string) bool {
	switch f {
	case "file_path", "symbol_name", "type", "summary", "description",
		"keywords", "signature", "status", "content",
		"is_test", "test_kind":
		return true
	}
	const prefix = "metadata."
	if len(f) > len(prefix) && f[:len(prefix)] == prefix {
		return true
	}
	return false
}

// validPredicateMatch returns true for the 7 closed match operators.
func validPredicateMatch(m string) bool {
	switch m {
	case "regex", "prefix", "suffix", "contains", "equals", "in", "tokens_match":
		return true
	}
	return false
}

// wirePipeline is the JSON-decode shape for ParsePipeline. Per-op slots
// stay as RawMessage so the dispatch switch can read the `op`
// discriminator before unmarshaling into a concrete type. Private — used
// only by ParsePipeline.
type wirePipeline struct {
	Pre  []json.RawMessage `json:"pre,omitempty"`
	Post []json.RawMessage `json:"post,omitempty"`
}

// opDiscriminator is the minimal envelope used to peek at the `op` field
// before dispatching to the concrete type.
type opDiscriminator struct {
	Op string `json:"op"`
}

// parseOp dispatches a single raw op JSON into the matching concrete type
// via a switch on the `op` discriminator. The DSL is closed at three ops
// — adding one means editing this switch, by design (no init-time
// registry, no sync mutex).
func parseOp(raw json.RawMessage) (Op, error) {
	var disc opDiscriminator
	if err := json.Unmarshal(raw, &disc); err != nil {
		return nil, fmt.Errorf("read op discriminator: %w", err)
	}
	switch disc.Op {
	case "filter":
		var op FilterOp
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, fmt.Errorf("decode filter op: %w", err)
		}
		return &op, nil
	case "score":
		var op ScoreOp
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, fmt.Errorf("decode score op: %w", err)
		}
		return &op, nil
	case "limit":
		var op LimitOp
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, fmt.Errorf("decode limit op: %w", err)
		}
		return &op, nil
	case "":
		return nil, errors.New("op missing required \"op\" discriminator field")
	default:
		return nil, fmt.Errorf("unknown op %q", disc.Op)
	}
}

// ParsePipeline parses the JSON wire format into a *Pipeline and runs
// Pipeline.Validate. Errors name the offending phase + index
// (e.g. `pre[2]: unknown op "frob"`). This is the single entry point for
// turning bytes into a validated Pipeline — Pipeline.UnmarshalJSON
// delegates here so encoding/json's default decoder validates on receipt.
func ParsePipeline(data []byte) (*Pipeline, error) {
	var wire wirePipeline
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("rerank pipeline: %w", err)
	}
	p := &Pipeline{}
	if len(wire.Pre) > 0 {
		p.Pre = make([]Op, 0, len(wire.Pre))
		for i, raw := range wire.Pre {
			op, err := parseOp(raw)
			if err != nil {
				return nil, fmt.Errorf("pre[%d]: %w", i, err)
			}
			p.Pre = append(p.Pre, op)
		}
	}
	if len(wire.Post) > 0 {
		p.Post = make([]Op, 0, len(wire.Post))
		for i, raw := range wire.Post {
			op, err := parseOp(raw)
			if err != nil {
				return nil, fmt.Errorf("post[%d]: %w", i, err)
			}
			p.Post = append(p.Post, op)
		}
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// Validate enforces pipeline-level invariants:
//   - LimitOp may not appear in Pre (positional rule).
//   - Every op's own Validate must pass (per-op enums + predicate trees).
//
// Errors name the offending phase + index. Validate is exported so direct
// constructions (tests, in-process callers) can validate without going
// through JSON, but the production path is ParsePipeline → Validate via
// UnmarshalJSON. There is no defense-in-depth Validate at the server
// callsite — UnmarshalJSON already does it.
func (p *Pipeline) Validate() error {
	for i, op := range p.Pre {
		if _, isLimit := op.(*LimitOp); isLimit {
			return fmt.Errorf("pre[%d]: limit op not allowed in pre", i)
		}
		if err := op.Validate(); err != nil {
			return fmt.Errorf("pre[%d]: %w", i, err)
		}
	}
	for i, op := range p.Post {
		if err := op.Validate(); err != nil {
			return fmt.Errorf("post[%d]: %w", i, err)
		}
	}
	return nil
}

// UnmarshalJSON delegates to ParsePipeline so encoding/json's default
// decoder validates on receipt. This is what makes `searchArgs.Strategies
// *Pipeline` decode-and-validate in a single step on the server.
func (p *Pipeline) UnmarshalJSON(data []byte) error {
	parsed, err := ParsePipeline(data)
	if err != nil {
		return err
	}
	*p = *parsed
	return nil
}
