// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	sitter "github.com/smacker/go-tree-sitter"
)

// calleeSpanScan is everything the normalization needs to know about a composed
// span's structure. These six fields are the entire contract: the cut, the
// elision and the retained legacy fallback read nothing else.
type calleeSpanScan struct {
	// LastCloseAtDepth0 is the index of the last `)` or `]` seen OUTSIDE a
	// quoted region and at brace depth zero, or -1.
	LastCloseAtDepth0 int
	// HasOpenAtDepth0 reports whether any `(` or `[` was seen outside a quoted
	// region at brace depth zero.
	HasOpenAtDepth0 bool
	// BraceRuns are the byte ranges of each balanced top-level `{...}` run.
	BraceRuns [][2]int
	// Balanced reports whether the whole span came out balanced — every quote
	// closed and every delimiter matched. A span that is not balanced is one
	// this structural read declines to interpret.
	Balanced bool
}

// scanCalleeSpan makes ONE quote-aware, brace-depth-aware pass over a composed
// callee span.
//
// QUOTE-AWARENESS IS LOAD-BEARING IN BOTH DIRECTIONS, and a text scan that
// lacks it gets this wrong in a way no corpus measurement catches. A brace
// elision that ignores string literals turns `rule.split('{')[0].match(...)`
// into an unbalanced span and DESTROYS the legitimate callee `match`; a
// delimiter cut that ignores them slices a shell command word at the `]` inside
// `"${BASH_SOURCE[0]}"`.
//
// BRACE-DEPTH-AWARENESS IS THE OTHER HALF. A depth-blind cut takes the last
// closing paren anywhere in the span, so a composite literal carrying a call in
// its body — `protoimpl.TypeBuilder{GoTypes: unsafe.Slice(x, 2), Depth: 1}` —
// has its type name sliced clean off. Counting the delimiters only at brace
// depth zero means that inner paren is never a cut point.
//
// ESCAPES ARE ASYMMETRIC BY DESIGN: they apply inside `'` and `"` and NOT
// inside backticks, because a Go raw string literal takes no escapes at all and
// consuming the byte after every backslash would swallow the closing delimiter
// of `C:\`. Checked in the other direction too — a JavaScript template literal
// carrying an escaped backtick parses identically either way.
func scanCalleeSpan(s string) calleeSpanScan {
	sc := calleeSpanScan{LastCloseAtDepth0: -1}
	st := spanScanState{runStart: -1}

	for i := 0; i < len(s); i++ {
		if st.quote != 0 {
			i = st.skipQuoted(s, i)
			continue
		}
		st.step(&sc, s[i], i)
	}

	sc.Balanced = st.quote == 0 && st.brace == 0 && st.paren == 0 && st.bracket == 0
	return sc
}

// spanScanState is scanCalleeSpan's running state: the open quote delimiter, the
// three nesting depths, and the start of the brace run in progress.
type spanScanState struct {
	quote                 byte // 0 when outside a quoted region
	brace, paren, bracket int
	runStart              int
}

// skipQuoted consumes the byte at i INSIDE a quoted region and returns the index
// the caller continues from — one past the escaped byte when the delimiter takes
// escapes, i otherwise.
func (st *spanScanState) skipQuoted(s string, i int) int {
	c := s[i]
	if c == '\\' && (st.quote == '\'' || st.quote == '"') {
		return i + 1 // the escaped byte is data, including a closing delimiter
	}
	if c == st.quote {
		st.quote = 0
	}
	return i
}

// step consumes the byte at i OUTSIDE a quoted region, recording what the scan's
// contract promises.
func (st *spanScanState) step(sc *calleeSpanScan, c byte, i int) {
	switch c {
	case '\'', '"', '`':
		st.quote = c
	case '{':
		if st.brace == 0 {
			st.runStart = i
		}
		st.brace++
	case '}':
		st.brace--
		if st.brace == 0 && st.runStart >= 0 {
			sc.BraceRuns = append(sc.BraceRuns, [2]int{st.runStart, i + 1})
			st.runStart = -1
		}
	case '(':
		st.paren++
		sc.HasOpenAtDepth0 = sc.HasOpenAtDepth0 || st.brace == 0
	case '[':
		st.bracket++
		sc.HasOpenAtDepth0 = sc.HasOpenAtDepth0 || st.brace == 0
	case ')':
		st.paren--
		if st.brace == 0 {
			sc.LastCloseAtDepth0 = i
		}
	case ']':
		st.bracket--
		if st.brace == 0 {
			sc.LastCloseAtDepth0 = i
		}
	}
}

// elideCalleeRuns removes the given byte ranges from s. The builder is
// allocated only when a run is actually elided, so the overwhelmingly common
// clean callee costs nothing.
func elideCalleeRuns(s string, runs [][2]int) string {
	if len(runs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, r := range runs {
		if r[0] < prev || r[1] > len(s) {
			continue
		}
		b.WriteString(s[prev:r[0]])
		prev = r[1]
	}
	b.WriteString(s[prev:])
	return b.String()
}

// dropChainOperators removes each maximal run of `ops` bytes whose FOLLOWING
// byte is in `follow`. A RUN rather than a single byte, because Kotlin's
// non-null assertion is `!!` and dropping one rune leaves `o!.length`, which is
// not a name either and is then declined outright — turning a repairable call
// into a lost one.
func dropChainOperators(s, ops, follow string) string {
	if ops == "" || !strings.ContainsAny(s, ops) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if strings.IndexByte(ops, s[i]) < 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && strings.IndexByte(ops, s[j]) >= 0 {
			j++
		}
		if j < len(s) && strings.IndexByte(follow, s[j]) >= 0 {
			i = j // the whole run goes
			continue
		}
		b.WriteString(s[i:j])
		i = j
	}
	return b.String()
}

// calleeSeparators are the runes that join a qualifier to a name in the
// spellings the chunker emits verbatim from source: `.` in most languages, `:`
// in Rust, C++ and PHP static calls and in Lua methods, and `>` in PHP's `->`.
const calleeSeparators = ".:>"

// calleeIsNameable reports whether s could be a name at all. The character set
// is derived from the corpora rather than guessed: Unicode letters and digits,
// plus `_ $ @ # -` and the separators. `#` is in it because JavaScript private
// fields such as `this.#e` are legitimate emitted callees; `-` because shell
// command names such as `golangci-lint` are, and because PHP's repaired
// `$o->a->b` needs it. extra carries the per-language additions — `?` and `!`
// for Elixir and Ruby predicate and bang names.
//
// A STRING WHOSE FIRST RUNE IS A SEPARATOR IS NOT NAMEABLE, which is what
// declines an anonymous object-literal receiver: `{a:1}.hasOwnProperty` elides
// to `.hasOwnProperty`, and a leading separator means the qualifier was thrown
// away rather than absent.
func calleeIsNameable(s, extra string) bool {
	if s == "" {
		return false
	}
	// The bare shell builtins, admitted as a named special case rather than by
	// widening the set for everyone.
	if s == ":" || s == "." {
		return true
	}
	if r0, _ := utf8.DecodeRuneInString(s); strings.ContainsRune(calleeSeparators, r0) {
		return false
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
		case strings.ContainsRune("_$@#-", r):
		case strings.ContainsRune(calleeSeparators, r):
		case extra != "" && strings.ContainsRune(extra, r):
		default:
			return false
		}
	}
	return true
}

// calleeIsBareName reports whether s carries NO qualifier at all, which is what
// decides whether the resolver sends it down the UNQUALIFIED ladder into the
// reference's own scope. It is the narrow companion to calleeIsNameable and the
// two MUST NOT be folded together: calleeIsNameable asks whether a string could
// be a name, this asks whether it names a receiver as well. It is also what
// makes the chain-operator repairs compatible with the declines by
// construction — `o.size`, `o.a.b`, `$o->a->b`, `a.b.C` and `o.dispose` are all
// QUALIFIED, so none is bare and none is declined.
func calleeIsBareName(s string) bool {
	return s != "" && !strings.ContainsAny(s, calleeSeparators)
}

// calleeReceiverElided answers ONE question from the AST: did the grammar put a
// receiver to the LEFT of this callee that the composed span does not cover?
//
// It walks at most three ancestors from the first `callee` capture. AN argStop
// HIT ENDS THE WALK — that is the fix, and it is not a refinement: the capture
// sits inside an argument list, so every enclosing call is a CALLER rather than
// a receiver, and a walk that merely CONTINUES past it climbs into the caller
// and declines a legitimate argument-position call.
//
// THE ORDER OF THE TWO TESTS WITHIN A HOP IS IMMATERIAL, because the two kind
// lists are disjoint by construction and no single ancestor can satisfy both.
// What separates a correct implementation from a defective one is termination,
// never order.
//
// THE STRICTLY-LESS COMPARISON is the other half of the predicate: a plain
// `plain(3)` has its call starting AT the capture and must NOT be declined,
// while `a.b(1).c(2)`'s outer call starts at `a` and must be. A `<=` here would
// delete every unqualified Lua and Groovy call in the repository.
func calleeReceiverElided(capture *sitter.Node, spanStart uint32, kinds, argStop []string) bool {
	if capture == nil || len(kinds) == 0 {
		return false
	}
	n := capture
	for range 3 {
		n = n.Parent()
		if n == nil {
			return false
		}
		kind := n.Type()
		if slices.Contains(argStop, kind) {
			return false
		}
		if slices.Contains(kinds, kind) && n.StartByte() < spanStart {
			return true
		}
	}
	return false
}

// calleeDeclined reports whether the language's profile declines this callee
// outright, so nothing is emitted for it. THE THREE RULES ARE ORDERED BY HOW
// MUCH THEY KNOW: the receiver-elision rule reads the tree, the chained-tail
// rule reads the cut, and the nameability rule reads only the string.
func calleeDeclined(callee string, prof calleeProfile, cutFired bool,
	capture *sitter.Node, spanStart uint32,
) bool {
	if !prof.DeclineNonName {
		return false
	}
	// THE RECEIVER-ELISION DECLINE. Groovy's `o?.size()` and Lua's
	// `a.b(1).c(2)` hand over a BARE method name whose receiver sits in a node
	// the Calls query never reads, so no signal carried out of the cut can see
	// it — the cut never fired. Each conjunct excludes a shape that must
	// survive: the length check keeps the rule inert for the seventeen
	// languages with no wrapper list, calleeIsBareName keeps Groovy's
	// `o?.a.b()` emitting its qualified `a.b`, and calleeReceiverElided
	// separates `o?.size()` from a genuinely unqualified `size()`.
	if len(prof.ReceiverWrappers) > 0 && calleeIsBareName(callee) &&
		calleeReceiverElided(capture, spanStart, prof.ReceiverWrappers, prof.ReceiverArgStop) {
		return true
	}
	// THE CHAINED-TAIL DECLINE. A bare name left behind by the cut names a
	// method on a receiver this emission threw away: `a.b(1).c(2)` emits `c`,
	// whose receiver is the RESULT of `a.b(1)`, a type no static read of this
	// layer knows. Such a name goes down the resolver's UNQUALIFIED ladder into
	// the reference's own scope and binds to whatever same-named declaration is
	// there, producing an edge indistinguishable from a real static binding.
	//
	// IT KEYS ON THE STRUCTURAL PROPERTY, NOT ON A TOKEN: "the cut fired and
	// what survived carries no qualifier", never "a chain operator was
	// present". That is what makes `(a?.b).F()` and the wholly undecorated
	// `(x.y).G()` come out the same way, as they must — both are produced by
	// the identical cut in the same ten languages. The subscript receivers
	// `arr[0].size()`, `d['k'].method()` and `h[:k].to_s` reach it through the
	// bracket half of that one cut and are in the same class.
	if cutFired && calleeIsBareName(callee) {
		return true
	}
	// A span that is not a name at all emits nothing rather than an index key
	// that can bind only by accident.
	return !calleeIsNameable(callee, prof.NameExtra)
}

// normalizeCallee turns a raw composed callee span into the spelling the CALLS
// edge will carry, or reports that the language's profile declines it.
//
// IT IS SHARED BY THE CALLS EMITTER AND EVERY FLOW-STEP ARM, and that sharing is
// the point rather than a convenience. A FLOWS_TO_ARG edge carries the callee
// spelling as its endpoint and is resolved against the SAME reference site the
// sibling CALLS edge uses, so a spelling differing by ONE CHARACTER resolves to
// a DIFFERENT declaration, silently, with every other gate green — and because
// the flow Evidence group key is built from that spelling, a divergence also
// splits the edge's GC identity. Sixteen hand-derived spellings is the failure
// this one derivation exists to prevent.
//
// AN ARM THAT CANNOT REACH THIS FUNCTION'S INPUTS EMITS NO STEP AT ALL, never a
// spelling of its own. ok=false is the arm's instruction to drop the step, the
// same instruction it is to extractCallEdges' loop.
func normalizeCallee(raw string, prof calleeProfile, lastCapture string,
	calleeNode *sitter.Node, spanStart uint32,
) (string, bool) {
	// The span reproduces the source's own separator verbatim, so `$o->do`,
	// `Bar::stat`, `obj:meth` and `obj.do` all emit exactly as written.
	//
	// EVERY whitespace rune is removed, not merely the ends. A qualified callee
	// written across lines — `recv.\n\t\tmethod` — carries the line break and the
	// indent INSIDE the span, where an end-only trim cannot reach them, and the
	// resulting name matches no index key. Stripping throughout strictly subsumes
	// the end-only trim: the pinned Lua grammar folds leading whitespace into node
	// extents, which is the case the trim was added for, and that leading
	// whitespace is removed here too.
	//
	// THE STRIP PRECEDES THE SEPARATOR TRIM BELOW, and the order is load-bearing.
	// A chained tail whose FIRST character is the line break — `page.locator('a')
	// \n    .filter(4)` composes a tail of "\n    .filter" — would stop the
	// separator TrimLeft immediately if it ran first, leaving ".filter":
	// whitespace-free, so a whitespace census reads it as clean, and unbindable,
	// so the defect survives the gate built to catch it.
	callee := stripSpace(raw)

	// CHAINED-CALL AND SUBSCRIPT FALLBACK. An open paren or an open bracket can
	// only appear between two callee captures when the composed span crossed an
	// argument list or a subscript — `obj.a(1).b`, `arr[0].size` — and that text
	// belongs to neither the qualifier nor the name. Cut after the LAST closing
	// delimiter and strip the separator characters that joined the tail to what
	// came before; when nothing follows that delimiter, fall back to the last kept
	// capture's own text.
	//
	// THE CUT IS QUOTE-AWARE AND BRACE-DEPTH-AWARE, and both halves are
	// load-bearing. A delimiter inside a string literal is DATA — a shell command
	// word `"${BASH_SOURCE[0]}"` used to be sliced at that `]` and emitted as the
	// garbage `}"` — and a delimiter inside a composite literal's BODY belongs to
	// the literal, so a depth-blind cut takes the paren closing
	// `unsafe.Slice(x, 2)` inside `protoimpl.TypeBuilder{...}` and slices the type
	// name clean off.
	//
	// THE CUT ITSELF RUNS FOR EVERY LANGUAGE, including one with no profile row;
	// the literal-body elision beside it is the profile-gated half.
	//
	// cutFired records that the span was reduced past an argument list or a
	// subscript, which is what the chained-tail decline below keys on: what
	// survives such a cut names a method on a receiver this emission threw away.
	cutFired := false
	sc := scanCalleeSpan(callee)
	switch {
	case sc.Balanced && sc.HasOpenAtDepth0 && sc.LastCloseAtDepth0 >= 0:
		callee, cutFired = cutCalleeTail(callee, sc.LastCloseAtDepth0, lastCapture), true
	case sc.Balanced:
		if prof.ElideLiteralBodies {
			callee = elideCalleeRuns(callee, sc.BraceRuns)
		}
	case strings.ContainsAny(callee, "(["):
		// A span the structural read declines to interpret — an unterminated quote
		// or an unbalanced delimiter, which a grammar produces from an ERROR node.
		// Pre-existing behavior is retained verbatim for it; the declines below
		// still decide whether the result is emittable, so nothing degraded reaches
		// the graph by this path.
		callee, cutFired = cutCalleeTail(callee, strings.LastIndexAny(callee, ")]"), lastCapture), true
	}

	// THE CUTSET OMITS `?` AND `!` DELIBERATELY. Adding them would "repair"
	// `o.get(1)?.getAttribute('x')` into a bare `getAttribute`, which binds a
	// same-named module-scope local as a BOUND edge where the unrepaired spelling
	// binds it as a dynamic one — upgrading a fabrication into the graph's
	// strongest claim. The optional-chain shapes this code DOES repair carry no
	// parenthesis, never reach the cut, and are handled by the operator drop
	// below.
	callee = dropChainOperators(callee, prof.ChainOps, prof.ChainFollow)

	if callee == "" {
		return "", false
	}
	// THE THREE DECLINES, applied before the caller's own bookkeeping so a
	// declined callee never reaches weightedCallEdges: a bare name whose receiver
	// the GRAMMAR elided, a bare name whose receiver THE CUT threw away, and a
	// span that is not a name at all. Each emits NOTHING rather than a spelling
	// that binds by accident, and all three are inert for a language with no
	// profile row.
	if calleeDeclined(callee, prof, cutFired, calleeNode, spanStart) {
		return "", false
	}
	return callee, true
}
