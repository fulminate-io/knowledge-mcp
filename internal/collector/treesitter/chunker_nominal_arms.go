// SPDX-License-Identifier: Apache-2.0

package treesitter

// NominalArmedLanguages returns the NOMINAL-STATIC languages carrying a
// qualifier-type arm and a declared-conformance capture arm.
//
// IT IS THE SINGLE DERIVATION OF "WHICH LANGUAGES THIS GROUP ARMED", and it is
// deliberately NOT the capability census. The census is the capability table
// over EVERY registered language and answers "does this language have a
// registered type-facts arm, for any purpose"; this list is one group's scope,
// and the two are held to agree for these six by a test rather than by one
// being derived from the other.
//
// What the six share is the property the shared walk is built on: each DECLARES
// its conformance in a clause the grammar carries, so an edge is a direct
// capture of that clause plus a name binding — never a method-set comparison.
func NominalArmedLanguages() []Language {
	return []Language{LangJava, LangKotlin, LangScala, LangCSharp, LangPHP, LangGroovy}
}

// nominalDeclaredSupertypes appends one captured supertype to a declaration's
// carrier, dropping a spelling the renderer declined.
//
// An empty spelling names nothing and could only ever resolve to nothing, so
// carrying it forward would inflate the downstream unresolvable count with
// entries that were never a supertype.
func nominalDeclaredSupertypes(out []DeclaredSupertype, text string, kind ConformanceKind) []DeclaredSupertype {
	if text == "" {
		return out
	}
	return append(out, DeclaredSupertype{Text: text, Kind: kind})
}

// nominalTypeFacts returns the carrier when at least one field was filled, and
// nil otherwise.
//
// NIL IS THE UNFILLED ANSWER EVERYWHERE, and returning it rather than an empty
// struct is what keeps a declaration that states no type facts costing no
// allocation — the same free-nothing contract the carrier's own fields document.
func nominalTypeFacts(facts *TypeFacts) *TypeFacts {
	if len(facts.Conforms) == 0 && len(facts.Fields) == 0 && !facts.IsInterface && !facts.PartialBody {
		return nil
	}
	return facts
}
