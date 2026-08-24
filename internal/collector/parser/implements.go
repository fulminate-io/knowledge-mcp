// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"maps"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// THIS DERIVATION IS A PORT OF A MEASURED PROTOTYPE, NOT AN INVENTION. Its
// structure — expand interface method sets through embeds, promote concrete
// method sets through embeds, invert to a name-to-candidates map, take the
// rarest method first, then intersect — mirrors the prototype under
// ~/.knowledge/treesitter-parity-evidence/fanout/analyze/, which is what produced
// the precision figures this work is scored against. Deviations from it are
// stated as deviations, with their reason, wherever they occur below.
//
// IT READS THE DECLARATION INDEX AND NOTHING ELSE. Every input is a resolved
// field on declRec — SigKey, Embeds, ExtEmbed, IsInterface — put there at index
// time against the DECLARING file's imports. Re-deriving any of them from a
// chunk here would resolve against the wrong file's imports and would silently
// disagree with the index everything else reads.

// implTypeKey identifies a type declaration: its resolution scope plus its base
// name. It is the same (scope, name) pair the index's typeRef carries, named
// separately because this pass uses it as a map key rather than as a reference.
type implTypeKey struct {
	Scope string
	Name  string
}

// implExpandKey keys the method-set memo by type AND by depth budget, so the
// interface stage and the concrete stage cannot answer each other's questions.
type implExpandKey struct {
	tk    implTypeKey
	limit int
}

// methodPair is one interface method spec and the concrete method satisfying it.
type methodPair struct {
	spec *declRec
	impl *declRec
}

// implementsPair is one derived satisfaction: a concrete type implements an
// interface, with the per-method attribution that justifies it.
type implementsPair struct {
	iface    *declRec
	concrete *declRec
	// methodSetSize is the CARDINALITY of the interface's EXPANDED method set,
	// promoted embeds included. A consumer needs it to weight the edge: a
	// one-method interface is satisfied by a great many types, and that is
	// correct Go rather than a defect to suppress.
	methodSetSize int
	methods       []methodPair
}

// implementsStats is the derivation's first-class output about what it could NOT
// decide. A reader must be able to tell "this interface has no implementers"
// from "this interface could not be decided", and only a counter can carry that.
type implementsStats struct {
	// Interfaces counts interfaces with a non-empty expanded method set.
	Interfaces int
	// Pairs counts derived satisfactions.
	Pairs int
	// GenericUndecided counts interfaces skipped because they carry TYPE
	// PARAMETERS, which a syntax-level signature comparison cannot unify. They
	// are UNDECIDED, not implementer-free, and a graph shows the same nothing for
	// both — which is why the count exists.
	GenericUndecided int
	// ExtEmbedPairs counts pairs derived from an interface whose method set is a
	// LOWER BOUND because an embed could not be expanded. Reported rather than
	// suppressed: these are the derivation's known false-positive exposure.
	ExtEmbedPairs int
}

// ifaceExpandDepth and concreteExpandDepth bound the two embed walks.
//
// A CYCLIC EMBED IS A COMPILE ERROR IN GO, so the bound is not protecting
// against legal source — it protects against a malformed or partially-indexed
// corpus, where a half-resolved embed chain could otherwise loop. The two values
// are the measured prototype's and are kept rather than unified, because
// changing either changes which pairs derive and therefore the measured numbers.
const (
	ifaceExpandDepth    = 4
	concreteExpandDepth = 6
)

// deriveImplements derives interface satisfaction over a COMPLETE declaration
// index. It must not be called before the index is built across every file: a
// partial index yields false negatives that look exactly like real ones.
func deriveImplements(ix *declIndex) ([]implementsPair, implementsStats) {
	var stats implementsStats
	if ix == nil {
		return nil, stats
	}
	typeRecs, declared := implIndexViews(ix)

	memo := make(map[implExpandKey]map[string]*declRec, len(typeRecs))
	// expand returns the method set of a type key, promoting embedded types
	// RECURSIVELY. One function serves both stages: an interface's own "methods"
	// are the specs indexed under its name, a concrete type's are the methods
	// indexed under its name, and both promote through the same Embeds field.
	//
	// A DIRECTLY-DECLARED METHOD WINS OVER A PROMOTED ONE, which is Go's own
	// shallowest-depth rule and the prototype's lookup order.
	//
	// THE MEMO IS KEYED BY (TYPE, LIMIT), not by type alone. The two stages run
	// different depth budgets, and a set computed under one budget is not the set
	// the other would compute — sharing one entry would let a concrete-stage
	// expansion answer an interface-stage question.
	//
	// AN EMPTY ENTRY IS WRITTEN BEFORE RECURSING, which is what breaks a cycle:
	// a re-entrant call finds the in-progress entry and returns it instead of
	// recursing forever. The depth bound is then a second guard rather than the
	// only one — a cyclic embed is a compile error in Go, so both exist for a
	// malformed or partially-indexed corpus rather than for legal source.
	var expand func(tk implTypeKey, depth, limit int) map[string]*declRec
	expand = func(tk implTypeKey, depth, limit int) map[string]*declRec {
		ek := implExpandKey{tk: tk, limit: limit}
		if m, ok := memo[ek]; ok {
			return m
		}
		if depth > limit {
			return nil
		}
		memo[ek] = map[string]*declRec{}
		acc := make(map[string]*declRec, len(declared[tk]))
		maps.Copy(acc, declared[tk])
		if rec := typeRecs[tk]; rec != nil {
			for _, e := range rec.Embeds {
				for name, impl := range expand(implTypeKey(e), depth+1, limit) {
					if _, direct := acc[name]; !direct {
						acc[name] = impl
					}
				}
			}
		}
		memo[ek] = acc
		return acc
	}

	concreteSets, byMethod := implConcreteUniverse(typeRecs, expand)
	ifaceKeys := implInterfaceKeys(typeRecs)

	var pairs []implementsPair
	for _, ik := range ifaceKeys {
		ifaceRec := typeRecs[ik]
		want := expand(ik, 0, ifaceExpandDepth)
		// AN EMPTY METHOD SET DERIVES NOTHING. Every type satisfies it, so a pair
		// would carry no information and the edge count would explode.
		if len(want) == 0 {
			continue
		}
		if implIsUndecidableGeneric(ifaceRec) {
			stats.GenericUndecided++
			continue
		}
		stats.Interfaces++
		extEmbed := implSetIsLowerBound(ix, typeRecs, ifaceRec)
		found := implMatch(ik, ifaceRec, want, concreteSets, byMethod, typeRecs)
		if extEmbed {
			stats.ExtEmbedPairs += len(found)
		}
		pairs = append(pairs, found...)
	}
	stats.Pairs = len(pairs)
	return pairs, stats
}

// implConcreteUniverse builds the candidate side: every non-interface type's
// promoted method set, plus the INVERSION from method name to the types
// declaring it.
//
// THE INVERSION IS WHAT KEEPS THIS PASS TRACTABLE. Measured universes are ~180
// interfaces by ~2,600 concrete types (and ~309 by ~3,100 on the second corpus);
// a naive interface-by-type double loop is hundreds of thousands of signature
// comparisons per collect. Taking an interface's RAREST method first bounds the
// candidate list before any signature is compared.
func implConcreteUniverse(
	typeRecs map[implTypeKey]*declRec,
	expand func(tk implTypeKey, depth, limit int) map[string]*declRec,
) (map[implTypeKey]map[string]*declRec, map[string][]implTypeKey) {
	concreteSets := make(map[implTypeKey]map[string]*declRec, len(typeRecs))
	byMethod := make(map[string][]implTypeKey, len(typeRecs))
	for tk, rec := range typeRecs {
		if rec.IsInterface {
			// An interface is never a candidate IMPLEMENTER.
			continue
		}
		set := expand(tk, 0, concreteExpandDepth)
		if len(set) == 0 {
			continue
		}
		concreteSets[tk] = set
		for name := range set {
			byMethod[name] = append(byMethod[name], tk)
		}
	}
	// Sorted so every derived slice is stable across runs regardless of map
	// iteration order.
	for name := range byMethod {
		sort.Slice(byMethod[name], func(i, j int) bool { return implKeyLess(byMethod[name][i], byMethod[name][j]) })
	}
	return concreteSets, byMethod
}

// implInterfaceKeys returns every interface type key, in a deterministic order.
func implInterfaceKeys(typeRecs map[implTypeKey]*declRec) []implTypeKey {
	out := make([]implTypeKey, 0, len(typeRecs))
	for tk, rec := range typeRecs {
		if rec.IsInterface {
			out = append(out, tk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return implKeyLess(out[i], out[j]) })
	return out
}

// implMatch intersects one interface's method set against the candidate types
// that declare its rarest method.
func implMatch(
	ik implTypeKey,
	ifaceRec *declRec,
	want map[string]*declRec,
	concreteSets map[implTypeKey]map[string]*declRec,
	byMethod map[string][]implTypeKey,
	typeRecs map[implTypeKey]*declRec,
) []implementsPair {
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		li, lj := len(byMethod[names[i]]), len(byMethod[names[j]])
		if li != lj {
			return li < lj
		}
		return names[i] < names[j]
	})

	// RULE (b): an interface with ANY unexported method in its expanded set can
	// only be satisfied from the package that declares that method, because Go
	// qualifies an unexported method name by its declaring package.
	confined := false
	for _, n := range names {
		if !implNameExported(n) {
			confined = true
			break
		}
	}

	var out []implementsPair
	for _, tk := range byMethod[names[0]] {
		if tk == ik {
			continue // a type never implements ITSELF
		}
		// RULE (b)'s SCOPE IS AN APPROXIMATION OF PACKAGE, NOT AN IDENTITY, and
		// saying so is the point. The index's unit is Go's directory scope, and
		// directory and package coincide in Go with ONE exception: an external
		// test package `foo_test` shares a directory with `foo`. So same-Scope is
		// very slightly WIDER than same-package and admits an external test type
		// as a candidate for an unexported-method interface it could not really
		// satisfy. That over-admission is bounded, it runs in the same direction
		// as the go/types oracle's own universe (built with Tests:true), and it is
		// ACCEPTED rather than corrected — narrowing it would need a package-clause
		// distinction the scope key does not carry.
		//
		// THERE IS A SECOND APPROXIMATION HERE, AND IT PUSHES THE OPPOSITE WAY.
		// When the unexported method that set `confined` was PROMOTED OUT OF AN
		// EMBEDDED INTERFACE declared elsewhere, the confinement is applied at
		// the EMBEDDING interface's scope — `ik.Scope` — while Go qualifies an
		// unexported method name by the package that DECLARES it, which is the
		// embedded interface's. So this confines to the embedder where Go
		// confines to the declarer. It is ACCEPTED as fidelity to the measured
		// prototype rather than corrected, because narrowing it would change the
		// derived pairs and move the very oracle scores this derivation's
		// acceptance floors are pinned to.
		if confined && tk.Scope != ik.Scope {
			continue
		}
		have := concreteSets[tk]
		methods := make([]methodPair, 0, len(names))
		ok := true
		for _, n := range names {
			impl, present := have[n]
			// RULE (a): same NAME and same RESOLVED SIGNATURE. SigKey equality is
			// the whole of it — no signature TEXT is compared anywhere, because two
			// packages spelling a type identically declare two different types and
			// only the resolved key tells them apart.
			if !present || impl.SigKey != want[n].SigKey {
				ok = false
				break
			}
			methods = append(methods, methodPair{spec: want[n], impl: impl})
		}
		if !ok {
			continue
		}
		concreteRec := typeRecs[tk]
		if concreteRec == nil {
			continue
		}
		out = append(out, implementsPair{
			iface:         ifaceRec,
			concrete:      concreteRec,
			methodSetSize: len(names),
			methods:       methods,
		})
	}
	return out
}

// implIndexViews splits the index into the two views this derivation needs: the
// type declarations themselves, and the methods declared under each of them.
//
// THERE IS NO DECLARATION-KIND FIELD TO FILTER ON, and none is added. A record
// with an empty Parent is a top-level declaration, which is a type OR a
// function, and the two are indistinguishable when the function has no
// signature. Admitting a function as a candidate type is INERT: it declares no
// methods and carries no embeds, so its method set is empty and it is dropped
// before any matching happens. Go also forbids a package declaring a type and a
// function of the same name, so a real type is never shadowed by one.
//
// IT ADMITS GO RECORDS ONLY, AND THE GATE IS A PROPERTY OF THE RECORD RATHER
// THAN OF WHO HAPPENS TO HAVE REGISTERED AN ARM. This derivation is Go
// method-set matching over Go's own rules, and two of them describe no other
// language: rule (a) compares resolved signature keys with `!=`, so two EMPTY
// keys compare EQUAL and a language whose facts carry no signature matches
// every method by name alone; and rule (b)'s confinement keys on Go's
// capitalization convention through implNameExported, which is not how any
// other language marks visibility. Keying the gate on the language that
// declared the record makes the scoping structural — before this, it held only
// because the Go arm was the sole registered type-facts arm, which is an
// accident a single registration elsewhere would silently undo.
func implIndexViews(ix *declIndex) (map[implTypeKey]*declRec, map[implTypeKey]map[string]*declRec) {
	typeRecs := make(map[implTypeKey]*declRec, len(ix.byID))
	declared := make(map[implTypeKey]map[string]*declRec, len(ix.byID))
	for k, recs := range ix.byKey {
		if len(recs) == 0 {
			continue
		}
		// A COLLISION GROUP TAKES ITS FIRST MEMBER, in the index's build order,
		// which is file order then in-file byte order — deterministic rather than a
		// map range. Two declarations under one key are build-tag variants of one
		// declaration far more often than they are two real ones.
		rec := recs[0]
		// THE LANGUAGE GATE, placed after the collision-group pick so ONE
		// statement covers both the type branch and the declared-member branch.
		if rec.Lang != treesitter.LangGo {
			continue
		}
		if k.Parent == "" {
			typeRecs[implTypeKey{Scope: k.Scope, Name: k.Name}] = rec
			continue
		}
		owner := implTypeKey{Scope: k.Scope, Name: k.Parent}
		if declared[owner] == nil {
			declared[owner] = map[string]*declRec{}
		}
		declared[owner][k.Name] = rec
	}
	return typeRecs, declared
}

// implSetIsLowerBound reports whether an interface's expanded method set is
// incomplete because some embed could not be expanded.
//
// ExtEmbed GATES NOTHING, AND THAT IS THE DISPOSITION rather than an oversight.
// An interface carrying it IS still matched and IS still derived; its method set
// is a LOWER BOUND, so a concrete type covering the KNOWN subset may not cover
// the real one, and those pairs are a known false-positive source. Deriving
// anyway is deliberate on two grounds: it is what the measured prototype does,
// so gating here would move the very numbers the acceptance floors are derived
// from; and the alternative — skipping the interface — converts a POSSIBLE false
// positive into a CERTAIN false negative on every interface embedding `error` or
// `io.Reader`, which is an extremely common shape. The exposure is REPORTED
// instead, on the stats line. TestDeriveImplements/extembed_still_derives pins
// this, and declRec.ExtEmbed's own doc comment names that subtest.
func implSetIsLowerBound(ix *declIndex, typeRecs map[implTypeKey]*declRec, rec *declRec) bool {
	if rec == nil {
		return false
	}
	if rec.ExtEmbed {
		return true
	}
	for _, e := range rec.Embeds {
		ek := implTypeKey(e)
		// AN EMBED THAT RESOLVED BUT NAMES NO DECLARATION IS EQUALLY
		// UNEXPANDABLE. ExtEmbed is set at index time from whether a spelling
		// resolved to a scope, which is not the same question as whether that
		// scope declares it — a type in an indexed directory that contributed no
		// declaration resolves cleanly and expands to nothing. The completed index
		// is the first place the stronger question can be asked, so it is asked
		// here rather than trusted from the flag alone.
		if len(ix.lookup(declKey{Scope: ek.Scope, Name: ek.Name})) == 0 {
			return true
		}
		// One level of propagation: an interface embedding an interface that
		// itself has an unexpandable embed is equally under-known.
		if sub := typeRecs[ek]; sub != nil && sub.ExtEmbed {
			return true
		}
	}
	return false
}

// implNameExported reports whether a Go identifier is exported.
//
// BY THE FIRST RUNE, NOT BY AN ASCII BYTE COMPARISON: Go's rule is that an
// identifier is exported when its first RUNE is an upper-case letter, so a
// leading underscore or a non-Latin lower-case letter is unexported and a byte
// range check gets both wrong.
func implNameExported(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// implIsUndecidableGeneric reports whether an interface carries type PARAMETERS,
// which a syntax-level signature comparison cannot unify.
//
// IT READS THE STRUCTURAL FLAG THE CHUNKER RECORDED, and two earlier inferential
// forms are worth naming because both were WRONG and both failed quietly:
//
//	"any leaf the index declares nowhere" — fires on every type from a directory
//	discovery excludes, generated protobuf above all. Measured on the frozen
//	knowledge corpus: 145 of 184 interfaces reported undecided, recall 3.3%.
//	"any SAME-SCOPE leaf the index declares nowhere" — narrower, and still wrong,
//	because a type ALIAS is a real same-package type the declaration query never
//	captures. It misclassified store.DB, which has no type parameters at all,
//	because DB's methods name the aliased store.Edge.
//
// A resolved signature simply does not carry the distinction; the parse tree
// does, so the answer is carried from there.
//
// A SKIPPED INTERFACE DERIVES THE SAME NOTHING IT WOULD DERIVE ANYWAY — a
// parameter and the implementer's concrete type never render the same key. The
// skip exists to make the outcome REPORTABLE: "could not decide" and "no
// implementers" look identical in a graph, and only a counter tells them apart.
func implIsUndecidableGeneric(rec *declRec) bool {
	return rec != nil && rec.IsGeneric
}

// implKeyLess orders type keys deterministically, so every derived slice is
// stable across runs regardless of map iteration order.
func implKeyLess(a, b implTypeKey) bool {
	if a.Scope != b.Scope {
		return a.Scope < b.Scope
	}
	return a.Name < b.Name
}
