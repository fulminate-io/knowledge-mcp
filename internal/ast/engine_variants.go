// SPDX-License-Identifier: Apache-2.0

// engine_variants.go — the union compile. Every context wrapper whose parse is
// ERROR-free AND that HOSTS the pattern becomes a candidate; the walker runs
// them all and stamps each match with the variant it came from.
//
// WHY A UNION. A pattern text is a fragment, and several of a grammar's parse
// contexts can accept the same fragment while meaning different things. Java
// `return $X;` is a return statement inside a method body and a (nonsense but
// grammatical) field declaration inside a class body; compiling to whichever
// context happened to be registered first made the pattern mean the one the
// caller did not write. The compiler now keeps every context that can express
// the fragment and the walk matches their union.
//
// PARSING IS NECESSARY BUT NOT SUFFICIENT — A WRAPPER MUST ALSO HOST. Splicing
// a fragment between a wrapper's Prefix and Suffix can produce an ERROR-free
// tree in which the smallest node covering the fragment spills into the
// wrapper's own bytes. That node is not what the caller wrote: matching it
// would require every target to carry the wrapper's punctuation too, which no
// real source does. Hosting is decided by three ordered rules, and the ORDER is
// load-bearing:
//
//  1. EXACT — the effective root's span equals the fragment's span. Every
//     single-construct pattern takes this rule.
//  2. TRAILING ABSORPTION — a named child starts at the fragment's first byte
//     and a run of ANONYMOUS siblings covers the rest of it. That child is the
//     root and the anonymous run is ABSORBED. tree-sitter-typescript puts a
//     class member's `;` in the class_body list rather than in the member node,
//     so without this rule no TS class-member pattern is expressible.
//  3. CONTAINER — the root strictly contains the fragment on both sides and
//     every out-of-span child is anonymous and lies inside the wrapper's own
//     Prefix or Suffix bytes. This is what keeps a BARE SIBLING SEQUENCE
//     working: go `$A()\n$B()` has no enclosing construct of its own, so its
//     root is the statement wrapper's block, which begins at the wrapper's `{`.
//     Rules 1 and 2 both decline it, and without rule 3 a pattern that finds
//     real sites today would become a hard compile error.
//
// Rule 2 runs before rule 3 on purpose: a class_body's only out-of-span
// children are its braces, so rule 3 would otherwise accept it and root the
// pattern at the container rather than at the member.
//
// ABSORBED AND OUT-OF-SPAN ARE DIFFERENT THINGS. Absorbed is text the USER
// WROTE that the match threw away, so it feeds the splice's dropped budget
// (RawMatch.DroppedSpans) — an absorbed token earns no alignment entry, and an
// identity template still carrying it would emit it beside source that already
// has one. Out-of-span is WRAPPER text that was never in the pattern; it feeds
// the dedupe key and nothing else, and it must never reach the dropped budget,
// which would license the splice to swallow real template bytes.
//
// DEDUPE IS ON A SERIALIZATION TRIPLE, NEVER ON sitter.Node.String(). Two
// wrappers routinely compile the same fragment to the same tree (go `$F($X)` is
// a call_expression under both the statement and the expression wrapper), and
// two identical candidates would double every match and hand replace.go two
// identical spans it reads as an overlap. The key is a recursive pre-order
// rendering over ALL children INCLUDING ANONYMOUS ONES, plus the absorbed and
// out-of-span span lists normalized to the fragment's start. The s-expression
// from Node.String() emits named nodes only, and this engine COMPARES anonymous
// tokens — keying on it would collapse two candidates that match differently.

package ast

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// patternVariant is one compiled candidate: the tree a single context wrapper
// produced, the context it came from, and what the hosting test had to set
// aside to reach the root.
//
// Tree is OWNED by the variant — whoever holds the slice must Close every
// element, including the ones a dedupe discarded.
type patternVariant struct {
	// Tree is the compiled pattern tree rooted at the hosting node.
	Tree *PatternTree

	// RootKind is the grammar type of the node the walker compares first,
	// or "" when that node is a placeholder (which matches any kind and so
	// pins no candidate prefilter).
	RootKind string

	// Contexts holds EVERY ContextWrapper.Context that compiled to this
	// variant's tree, in cfg.Wrappers order — the caller-facing vocabulary a
	// match is stamped with and a pin selects on by MEMBERSHIP. It is a set
	// rather than a scalar because several wrappers routinely compile a
	// fragment to the identical tree and the dedupe merges them: a scalar
	// would name whichever wrapper happened to be registered first, which is
	// a fact about the registry rather than about the pattern.
	Contexts []string

	// Wrappers holds the ContextWrapper.Names that produced this variant,
	// index-aligned with Contexts, for compile-failure text and disclosure.
	Wrappers []string

	// Absorbed holds the PATTERN-side spans of the trailing anonymous
	// tokens hosting rule 2 handed back to the wrapper's container. Seeded
	// into RawMatch.DroppedSpans so the splice can consume a template token
	// repeating one the match never compared.
	Absorbed []byteRange

	// OutOfSpan holds the WRAPPER-side spans of anonymous tokens inside the
	// root but outside the fragment, recorded by hosting rule 3. It feeds
	// the dedupe key and NOTHING else: a caller cannot act on the byte
	// offsets of a wrapper they never wrote, and letting it reach the
	// dropped budget would license the splice to delete real template text.
	OutOfSpan []byteRange
}

// Close releases the variant's tree. Nil-safe.
func (v *patternVariant) Close() {
	if v == nil {
		return
	}
	v.Tree.Close()
	v.Tree = nil
}

// compilePatternVariants is the union compiler. It substitutes placeholders
// once, then visits EVERY wrapper in cfg.Wrappers, keeping each one whose parse
// is ERROR-free and which hosts the fragment. Candidates come back in
// cfg.Wrappers order with structural duplicates removed.
//
// pinContext, when non-empty, restricts the union to variants carrying that
// context. The filter runs AFTER the full enumeration rather than skipping
// wrappers up front: a pin that selects nothing must be able to name the
// contexts that DID produce a candidate, which is unknowable if the excluded
// wrappers were never compiled. Every excluded wrapper still appears in the
// compile failure with the exclusion as its reason.
//
// On zero candidates the error names every wrapper tried with its Context and
// its specific rejection reason.
//
// It returns TWO variant sets: the kept candidates the walk runs, and the
// NARROWED set — member-context variants the keyword rule dropped, retained so
// the disclosure can name what was dropped and why. The narrowed set is empty on
// almost every compile. Its trees are owned by the caller exactly as the kept
// ones are: CompiledPattern.Close releases both.
func compilePatternVariants(ctx context.Context, pat Pattern, cfg LangConfig, pinContext string) ([]patternVariant, []patternVariant, error) {
	if pat.Source == "" {
		return nil, nil, errParseEmpty
	}
	if len(cfg.Wrappers) == 0 {
		return nil, nil, fmt.Errorf("ast/engine: LangConfig for %q has no wrappers", cfg.Lang)
	}

	subst, placeholderRanges := substitutePlaceholders(pat, cfg)

	parser := treesitter.NewParser()
	defer parser.Close()

	var (
		variants []patternVariant
		names    []string
		rejects  []string
		seen     = make(map[string]int, len(cfg.Wrappers))
	)
	for _, w := range cfg.Wrappers {
		names = append(names, w.Name)
		v, reason, err := compileUnderWrapper(ctx, parser, wrapperCompile{
			wrapper: w,
			subst:   subst,
			subs:    placeholderRanges,
			cfg:     cfg,
		})
		if err != nil {
			closeVariants(variants)
			return nil, nil, err
		}
		if reason != "" {
			rejects = append(rejects, fmt.Sprintf("%s[%s]: %s", w.Name, w.Context, reason))
			continue
		}
		key := variantKey(v, uint32(len(w.Prefix)))
		if at, dup := seen[key]; dup {
			// Another wrapper compiled the SAME tree: the fragment is
			// expressible in both contexts and means the same thing in each,
			// so the survivor records both. Discarding the loser's context
			// would report the registry's ordering as if it were a property of
			// the pattern.
			variants[at].Contexts = append(variants[at].Contexts, w.Context)
			variants[at].Wrappers = append(variants[at].Wrappers, w.Name)
			v.Close()
			continue
		}
		seen[key] = len(variants)
		variants = append(variants, v)
	}
	if len(variants) == 0 {
		return nil, nil, fmt.Errorf("%w (tried %s; %s)",
			errCompileNoWrapper, strings.Join(names, ","), strings.Join(rejects, "; "))
	}
	// The keyword narrowing runs BETWEEN the dedupe and the pin, for the two
	// reasons the pin already needs the same position: the full enumeration must
	// have happened so variants can be compared against each other, and every
	// dropped variant must survive to be disclosed.
	kept, narrowed := narrowMemberKeywordVariants(variants, pinContext)
	pinned, err := applyContextPin(kept, pinContext, names, rejects)
	if err != nil {
		closeVariants(narrowed)
		return nil, nil, err
	}
	return pinned, narrowed, nil
}

// wrapperCompile bundles compileUnderWrapper's inputs so the signature stays
// readable — every field is per-call constant except the wrapper itself.
type wrapperCompile struct {
	wrapper ContextWrapper
	subst   string
	subs    []substitution
	cfg     LangConfig
}

// compileUnderWrapper attempts one wrapper. It returns the variant on success,
// or a non-empty rejection reason naming why this wrapper contributed no
// candidate. A non-nil error is a compiler fault, not a rejection, and aborts
// the whole compile.
func compileUnderWrapper(ctx context.Context, parser *treesitter.Parser, a wrapperCompile) (patternVariant, string, error) {
	w := a.wrapper
	full := w.Prefix + a.subst + w.Suffix
	tree, err := parser.Parse(ctx, []byte(full), a.cfg.Lang)
	if err != nil {
		return patternVariant{}, fmt.Sprintf("parse error: %v", err), nil
	}
	root := tree.RootNode()
	if root == nil {
		tree.Close()
		return patternVariant{}, "parse error: parsed tree has no root", nil
	}
	if root.HasError() {
		summary := root.String()
		tree.Close()
		return patternVariant{}, fmt.Sprintf("parse error (ERROR-node summary: %s)", summary), nil
	}

	userStart := uint32(len(w.Prefix))
	userEnd := userStart + uint32(len(a.subst))
	host, ok := hostsPattern(root, userStart, userEnd)
	if !ok {
		reason := fmt.Sprintf("parsed but did not host: root %s spans wrapper text",
			coveringKind(root, userStart, userEnd))
		tree.Close()
		return patternVariant{}, reason, nil
	}

	pt, perr := buildPatternTree(tree, full, w, a.subs, a.cfg, host.root, host.depth)
	if perr != nil {
		tree.Close()
		return patternVariant{}, "", perr
	}
	kind, _ := patternRootKind(pt)
	return patternVariant{
		Tree:      pt,
		RootKind:  kind,
		Contexts:  []string{w.Context},
		Wrappers:  []string{w.Name},
		Absorbed:  host.absorbed,
		OutOfSpan: host.outOfSpan,
	}, "", nil
}

// closeVariants releases every tree in a partially built candidate set.
func closeVariants(variants []patternVariant) {
	for i := range variants {
		variants[i].Close()
	}
}

// patternRootKind returns the grammar type of the node the walker compares
// first and whether reaching it descended through same-span wrappers. The kind
// is "" when that node is a placeholder: a placeholder matches any type, so
// there is no kind to prefilter or disclose.
func patternRootKind(pt *PatternTree) (string, bool) {
	if pt == nil || pt.Root == nil {
		return "", false
	}
	eff := effectivePatternNode(pt.Root)
	if _, isPlaceholder := lookupPlaceholder(pt, eff); isPlaceholder {
		return "", false
	}
	return eff.Type(), eff != pt.Root
}

// hosting is the outcome of the three-rule hosting test: the node that becomes
// the pattern root, its depth below the parse root, and the spans each rule set
// aside.
type hosting struct {
	root      *sitter.Node
	depth     int
	absorbed  []byteRange
	outOfSpan []byteRange
}

// hostsPattern applies the three hosting rules in order against the smallest
// node covering [userStart, userEnd) — the fragment's span inside the wrapped
// source. Anything the three rules decline does not host, and a leading
// mismatch carrying named out-of-span content is always a rejection.
func hostsPattern(parseRoot *sitter.Node, userStart, userEnd uint32) (hosting, bool) {
	eff, depth := smallestNodeCovering(parseRoot, userStart, userEnd, 0)
	if eff == nil {
		return hosting{}, false
	}
	if eff.StartByte() == userStart && eff.EndByte() == userEnd {
		return hosting{root: eff, depth: depth}, true
	}
	if h, ok := hostByAbsorption(eff, depth, userStart, userEnd); ok {
		return h, true
	}
	return hostByContainer(eff, depth, userStart, userEnd)
}

// hostByAbsorption is hosting rule 2. The fragment is hosted by a NAMED child
// starting at its first byte when the only thing between that child's end and
// the fragment's end is a run of anonymous tokens the container owns.
//
// A NAMED leftover is never absorbed: absorbing one would silently delete a
// construct the caller wrote, and a deletion — unlike a duplication — does not
// show up in a diff of what the pattern matched.
func hostByAbsorption(eff *sitter.Node, depth int, userStart, userEnd uint32) (hosting, bool) {
	children := rawChildren(eff)
	host := -1
	for i, c := range children {
		if c.IsNamed() && c.StartByte() == userStart && c.EndByte() < userEnd {
			host = i
		}
	}
	if host < 0 {
		return hosting{}, false
	}
	for _, lead := range children[:host] {
		if lead.IsNamed() {
			return hosting{}, false
		}
	}

	var absorbed []byteRange
	reach := children[host].EndByte()
	i := host + 1
	for ; i < len(children); i++ {
		c := children[i]
		if c.StartByte() >= userEnd {
			break
		}
		if c.IsNamed() || c.StartByte() < userStart || c.EndByte() > userEnd {
			return hosting{}, false
		}
		absorbed = append(absorbed, byteRange{Start: c.StartByte(), End: c.EndByte()})
		reach = c.EndByte()
	}
	if reach != userEnd {
		return hosting{}, false
	}
	for ; i < len(children); i++ {
		if children[i].IsNamed() || !withinWrapperBytes(children[i], userStart, userEnd) {
			return hosting{}, false
		}
	}
	return hosting{root: children[host], depth: depth + 1, absorbed: absorbed}, true
}

// hostByContainer is hosting rule 3. A fragment with no enclosing construct of
// its own — a bare sibling sequence — is hosted by the wrapper's own container
// when everything of that container lying outside the fragment is anonymous
// wrapper punctuation. Preserving this rule preserves patterns that find real
// sites today; the two cases the hosting test exists to reject fail it on two
// independent legs each (a root that ends exactly at the fragment is not a
// strict container, and a NAMED out-of-span child is never wrapper text).
func hostByContainer(eff *sitter.Node, depth int, userStart, userEnd uint32) (hosting, bool) {
	if eff.StartByte() >= userStart || eff.EndByte() <= userEnd {
		return hosting{}, false
	}
	var outOfSpan []byteRange
	for _, c := range rawChildren(eff) {
		if c.StartByte() >= userStart && c.EndByte() <= userEnd {
			continue
		}
		if c.IsNamed() || !withinWrapperBytes(c, userStart, userEnd) {
			return hosting{}, false
		}
		outOfSpan = append(outOfSpan, byteRange{Start: c.StartByte(), End: c.EndByte()})
	}
	return hosting{root: eff, depth: depth, outOfSpan: outOfSpan}, true
}

// withinWrapperBytes reports whether n lies entirely inside the wrapper's own
// text. The wrapped source is Prefix + fragment + Suffix, so the Prefix is
// exactly [0, userStart) and the Suffix exactly [userEnd, end).
func withinWrapperBytes(n *sitter.Node, userStart, userEnd uint32) bool {
	return n.EndByte() <= userStart || n.StartByte() >= userEnd
}

// coveringKind names the root a wrapper produced for a fragment it did not
// host, for the compile-failure message.
func coveringKind(parseRoot *sitter.Node, userStart, userEnd uint32) string {
	eff, _ := smallestNodeCovering(parseRoot, userStart, userEnd, 0)
	if eff == nil {
		return "<none>"
	}
	return eff.Type()
}

// rawChildren returns every child of n in source order, ANONYMOUS TOKENS
// INCLUDED. The hosting rules and the dedupe key both need them: a wrapper's
// own punctuation is exactly what shows up as an anonymous child.
func rawChildren(n *sitter.Node) []*sitter.Node {
	count := int(n.ChildCount())
	out := make([]*sitter.Node, 0, count)
	for i := range count {
		if c := n.Child(i); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// variantKey is the dedupe key: the pattern root's full serialization plus the
// absorbed and out-of-span span lists, both normalized to userStart so two
// wrappers with different Prefix lengths can compare equal.
func variantKey(v patternVariant, userStart uint32) string {
	var b strings.Builder
	serializePattern(v.Tree.Root, &b)
	b.WriteString("|absorbed:")
	writeRelativeSpans(&b, v.Absorbed, userStart)
	b.WriteString("|outofspan:")
	writeRelativeSpans(&b, v.OutOfSpan, userStart)
	return b.String()
}

// serializePattern writes a recursive pre-order rendering of n over ALL its
// children, anonymous ones included. Node types only, so the rendering carries
// no byte offsets and two wrappers' trees for the same fragment compare equal.
func serializePattern(n *sitter.Node, b *strings.Builder) {
	if n == nil {
		b.WriteString("()")
		return
	}
	b.WriteByte('(')
	b.WriteString(n.Type())
	for i := range int(n.ChildCount()) {
		b.WriteByte(' ')
		serializePattern(n.Child(i), b)
	}
	b.WriteByte(')')
}

// writeRelativeSpans renders spans as offsets from origin. Out-of-span spans
// sit inside the wrapper and so go negative on the leading side, which is why
// the arithmetic is signed.
func writeRelativeSpans(b *strings.Builder, spans []byteRange, origin uint32) {
	for _, s := range spans {
		b.WriteString(strconv.FormatInt(int64(s.Start)-int64(origin), 10))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(int64(s.End)-int64(origin), 10))
		b.WriteByte(',')
	}
}
