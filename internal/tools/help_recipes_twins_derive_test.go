// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// help_recipes_twins_derive_test.go DERIVES the twin pairing from the corpus and
// holds the declared table to it.
//
// WHY THE TABLE ALONE WAS NOT ENOUGH. recipeBodyTwins is data: two pairs, named
// by hand. It proves the pairs it lists agree, and it says nothing at all about
// a pair it does not list. A body copied into a second carrier after it was
// written is asserted by nothing — the census reads each body's emit fields and
// the interpreted runs read each body's rows, so two copies pass on their own
// terms, which is the exact silence the twin gate was added to break one level
// up. The only prompt is indirect: a carrier's body count moves and its failure
// message sends an author to a file without naming this table.
//
// SO THE PAIRING IS COMPUTED, AND THE TABLE IS ASSERTED EQUAL TO IT. Both
// directions are failures: a declared pair the derivation does not find (the
// copies diverged, or the table names a body that moved) and a derived pair the
// table does not declare (a body was copied into a second carrier and nobody
// said so). Neither can be satisfied by silence.
//
// THREE KEYS, AND EACH ONE EARNS ITS PLACE.
//
//	raw       — the folded body text. Catches a verbatim copy.
//	stripped  — the same text with every `identity := <head>.<field>` removed.
//	            Catches the pair the identity fix itself could have split, when
//	            one copy is keyed and the other is not yet.
//	skeleton  — the PARSED body with every filter rule dropped, the identity
//	            field deleted from every emit, and every source position
//	            stripped. This is the carrier for the defect the twin gate was
//	            written after: two copies differing only by a kind filter hash
//	            the same here and nowhere else, so the pair is still found while
//	            the pair's own equality assertion reports the divergence.
//
// A pair is derived when the two bodies agree on ANY key, and the failure names
// which. The skeleton is deliberately coarse in one direction only: it drops
// filters and the identity, and it keeps every emit's node type and its
// remaining field names, so renaming an emitted column is a different body to a
// reader and is a different body here too.

// recipeCorpusBody is one body of one carrier, at the index that carrier's own
// extractor returned it. The index is what a failure message needs to send a
// reader to the body without quoting the whole thing twice.
type recipeCorpusBody struct {
	carrier string
	index   int
	body    string
}

func (b recipeCorpusBody) label() string {
	return fmt.Sprintf("%s body %d (%s)", b.carrier, b.index, firstLine(b.body))
}

// recipeCorpus flattens all three carriers into one addressable list, through
// the same recipeCarriers the census and the refusal gate use — so the corpus
// this derivation runs over is the corpus those gates run over, including their
// per-carrier count floors.
func recipeCorpus(t *testing.T) []recipeCorpusBody {
	t.Helper()
	var corpus []recipeCorpusBody
	for _, c := range recipeCarriers(t) {
		for i, b := range c.bodies {
			corpus = append(corpus, recipeCorpusBody{carrier: c.name, index: i, body: b})
		}
	}
	return corpus
}

// twinKeyNames labels the three keys in the order twinKeys returns them, so a
// failure can say which one paired two bodies rather than only that something
// did.
var twinKeyNames = [3]string{"raw", "identity-stripped", "skeleton"}

// twinKeys computes the three collision keys for one body.
func twinKeys(t *testing.T, b recipeCorpusBody) [3]string {
	t.Helper()
	stripped, _ := withoutIdentityField(b.body)
	return [3]string{
		foldBodyLines(b.body),
		foldBodyLines(stripped),
		recipeSkeleton(t, b),
	}
}

// recipeSkeleton renders a parsed body canonically with every filter rule
// dropped, the identity field deleted from every emit, and every source
// position stripped.
//
// POSITIONS ARE STRIPPED BY CONSTRUCTION, not by zeroing: this renders the
// fields it names and no Pos is among them, so two copies at different line
// offsets in different files cannot be separated by where they sit.
//
// AN UNKNOWN RULE KIND IS FATAL. A grammar that grows a rule this switch does
// not render would otherwise skeletonize two different bodies to the same
// string and manufacture a pair, which is the failure this file exists to
// prevent in the other direction.
func recipeSkeleton(t *testing.T, b recipeCorpusBody) string {
	t.Helper()
	r, err := recipe.Parse([]byte(b.body))
	if err != nil {
		t.Fatalf("%s does not parse, so it cannot be skeletonized: %v\n---\n%s", b.label(), err, b.body)
	}
	var out strings.Builder
	for _, rule := range r.Rules {
		switch v := rule.(type) {
		case recipe.RuleFilter:
			// Dropped on purpose: see the key table in this file's header.
		case recipe.RuleSelect:
			fmt.Fprintf(&out, "select %s where %s\n", v.NodeType, whereSkeleton(v.Where))
		case recipe.RuleTraverse:
			fmt.Fprintf(&out, "traverse %s %s as %s\n", v.EdgeType, v.Direction, v.As)
		case recipe.RuleWalk:
			fmt.Fprintf(&out, "walk %s as %s\n", v.EdgeType, v.As)
		case recipe.RuleBind:
			fmt.Fprintf(&out, "bind %s = %s\n", v.Var, exprSkeleton(v.Value))
		case recipe.RuleGroupBy:
			fmt.Fprintf(&out, "group_by %s\n", exprSkeleton(v.Key))
		case recipe.RuleEmit:
			fmt.Fprintf(&out, "emit %s as %s {%s}\n", v.NodeType, v.As, emitFieldsSkeleton(v.Fields))
		case recipe.RuleLookup:
			fmt.Fprintf(&out, "lookup %s as %s\n", v.NodeType, v.As)
		case recipe.RuleLink:
			fmt.Fprintf(&out, "link %s %s %s\n", exprSkeleton(v.From), v.Rel, exprSkeleton(v.To))
		case recipe.RuleSourceRef:
			fmt.Fprintf(&out, "source_ref %s\n", exprSkeleton(v.Ref))
		default:
			t.Fatalf("%s carries rule kind %T, which this skeleton does not render — an unrendered rule collapses two different bodies onto one key and manufactures a twin pair; add its case here",
				b.label(), rule)
		}
	}
	return out.String()
}

// emitFieldsSkeleton renders an emit's fields in key order with the identity
// field removed.
//
// THE IDENTITY IS DROPPED, NOT ITS EXPRESSION NORMALIZED. Two copies of one
// body keyed on different heads (`page.id` against `node.id`) are the same body
// to a reader deciding whether a treatment applies to both, and the raw key
// still separates them when they genuinely differ.
func emitFieldsSkeleton(fields map[string]recipe.Expr) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k == "identity" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+exprSkeleton(fields[k]))
	}
	return strings.Join(parts, ",")
}

// whereSkeleton renders a where-tree canonically. WhereNode is a plain struct
// with json tags and no Position anywhere in it, so its marshaling is both
// deterministic (Go emits struct fields in declaration order) and already
// position-free.
func whereSkeleton(w *recipe.WhereNode) string {
	if w == nil {
		return "-"
	}
	b, err := json.Marshal(w)
	if err != nil {
		return "unmarshalable:" + err.Error()
	}
	return string(b)
}

// exprSkeleton renders an expression canonically, position-free.
func exprSkeleton(e recipe.Expr) string {
	switch v := e.(type) {
	case recipe.ExprField:
		return strings.Join(v.Path, ".")
	case recipe.ExprLit:
		return "'" + v.Value + "'"
	case recipe.ExprVar:
		return "$" + v.Name
	case recipe.ExprRegex:
		op := "~="
		if v.Negate {
			op = "!~"
		}
		return exprSkeleton(v.LHS) + op + "/" + v.Pattern + "/"
	case recipe.ExprFunc:
		args := make([]string, 0, len(v.Args))
		for _, a := range v.Args {
			args = append(args, exprSkeleton(a))
		}
		return v.Name + "(" + strings.Join(args, ",") + ")"
	case nil:
		return "-"
	}
	return fmt.Sprintf("unrendered:%T", e)
}

// derivedTwinPair is one pair the derivation found, addressed by the two corpus
// positions and carrying the keys that paired them.
type derivedTwinPair struct {
	lo, hi int
	keys   []string
}

// deriveTwinPairs groups the corpus by each key in turn and returns every pair
// of bodies that collide on at least one.
func deriveTwinPairs(t *testing.T, corpus []recipeCorpusBody) map[[2]int]*derivedTwinPair {
	t.Helper()
	keys := make([][3]string, len(corpus))
	for i, b := range corpus {
		keys[i] = twinKeys(t, b)
	}
	found := map[[2]int]*derivedTwinPair{}
	for k := range twinKeyNames {
		byValue := map[string][]int{}
		for i := range corpus {
			byValue[keys[i][k]] = append(byValue[keys[i][k]], i)
		}
		for _, group := range byValue {
			for a := range group {
				for b := a + 1; b < len(group); b++ {
					id := [2]int{group[a], group[b]}
					if found[id] == nil {
						found[id] = &derivedTwinPair{lo: group[a], hi: group[b]}
					}
					found[id].keys = append(found[id].keys, twinKeyNames[k])
				}
			}
		}
	}
	return found
}

// locateDeclaredSide resolves one declared side to its position in the corpus,
// by the carrier the corpus knows it as and the fragment the table locates it
// with. It is fatal on zero or more than one match for the same reason
// oneBodyContaining is: an ambiguous locator points the assertion at a
// different body than the table named.
func locateDeclaredSide(t *testing.T, corpus []recipeCorpusBody, s recipeBodySide) int {
	t.Helper()
	var hits []int
	for i, b := range corpus {
		if b.carrier == s.corpusName && strings.Contains(b.body, s.fragment) {
			hits = append(hits, i)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("the twin table locates a %s body by %q and the corpus has %d of them, want exactly 1 — the table's locator no longer picks one body out of that carrier",
			s.carrier, s.fragment, len(hits))
	}
	return hits[0]
}

// TestRecipeCarriers_TwinTableIsTheDerivedPairing asserts the declared table and
// the derivation agree, in both directions.
func TestRecipeCarriers_TwinTableIsTheDerivedPairing(t *testing.T) {
	corpus := recipeCorpus(t)
	if len(corpus) < len(recipeBodyTwins)*2 {
		t.Fatalf("the corpus holds %d bodies, too few to carry the %d declared pairs — the derivation below would run over the wrong population",
			len(corpus), len(recipeBodyTwins))
	}
	derived := deriveTwinPairs(t, corpus)

	declared := map[[2]int]string{}
	for _, tw := range recipeBodyTwins {
		l := locateDeclaredSide(t, corpus, tw.left)
		r := locateDeclaredSide(t, corpus, tw.right)
		id := [2]int{min(l, r), max(l, r)}
		if prior, dup := declared[id]; dup {
			t.Errorf("the twin table declares the same pair twice, as %q and as %q", prior, tw.name)
		}
		declared[id] = tw.name
	}

	for id, name := range declared {
		if derived[id] == nil {
			t.Errorf("the table declares %q as a pair and NO key derives it: %s and %s collide on none of %v — either the two copies diverged by more than a filter and an identity, or the table names a pair that is no longer one\n--- %s\n%s\n--- %s\n%s",
				name, corpus[id[0]].label(), corpus[id[1]].label(), twinKeyNames,
				corpus[id[0]].label(), foldBodyLines(corpus[id[0]].body),
				corpus[id[1]].label(), foldBodyLines(corpus[id[1]].body))
		}
	}

	for id, d := range derived {
		if _, ok := declared[id]; ok {
			continue
		}
		t.Errorf("the derivation pairs %s with %s on the %v key(s) and the twin table declares no such pair — a body carried by a second surface is a twin whether or not anyone said so, and an undeclared one is asserted by nothing; add it to recipeBodyTwins with the reason the pair exists, or diverge the copies\n--- %s\n%s",
			corpus[d.lo].label(), corpus[d.hi].label(), d.keys,
			corpus[d.lo].label(), foldBodyLines(corpus[d.lo].body))
	}

	t.Logf("derived %d twin pair(s) from %d bodies over %v; the table declares %d",
		len(derived), len(corpus), twinKeyNames, len(declared))
}

// TestRecipeCarriers_TwinDerivationDiscriminates is the control on the
// derivation itself: without it, a keying bug that hashed every body to one
// value would derive every pair, and the equality assertion above would report
// a wall of undeclared pairs rather than a broken instrument.
//
// SO IT ASSERTS BOTH DIRECTIONS OF DISCRIMINATION IN ONE RUN: the corpus yields
// far fewer pairs than the complete graph over it, and a body compared against
// ITSELF collides on all three keys. A key function returning a constant fails
// the first; one returning the body's identity fails neither and is caught by
// the pair count, which is why the first bound is tight rather than nominal.
func TestRecipeCarriers_TwinDerivationDiscriminates(t *testing.T) {
	corpus := recipeCorpus(t)
	derived := deriveTwinPairs(t, corpus)

	complete := len(corpus) * (len(corpus) - 1) / 2
	if len(derived) >= complete/2 {
		t.Errorf("the derivation paired %d of the %d possible body pairs, which is not a pairing — a key that collapses unrelated bodies onto one value reads as a corpus full of twins",
			len(derived), complete)
	}

	for _, b := range corpus {
		self := twinKeys(t, b)
		again := twinKeys(t, b)
		if self != again {
			t.Errorf("%s hashes to different keys on two calls (%v then %v), so the derivation is not a function of the body — a map iteration order reached one of the renderers",
				b.label(), self, again)
		}
		if slices.Contains(self[:], "") {
			t.Errorf("%s yields an empty key among %v, and every empty key collides with every other — an empty raw body or a skeleton that rendered no rule", b.label(), self)
		}
	}
	t.Logf("%d bodies, %d derived pairs out of %d possible", len(corpus), len(derived), complete)
}
