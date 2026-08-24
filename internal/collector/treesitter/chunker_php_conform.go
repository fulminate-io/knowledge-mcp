// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterPHPTypeFacts()
}

// RegisterPHPTypeFacts installs the php type-facts arm, exported for the same
// restore-not-delete reason as the qualifier-type registrar.
func RegisterPHPTypeFacts() {
	RegisterTypeFacts(LangPHP, phpTypeFacts)
}

// phpTypeFacts records a php declaration's declared supertypes, whether it is
// itself a contract, and its property types.
//
// PHP WRITES CONFORMANCE IN THREE PLACES, each a distinct NAMED node kind — a
// base clause, an interface clause, and an in-body trait use — which makes it
// the one language in this group whose clause kind is fully recoverable from
// the tree rather than inferred or declined.
//
// A TRAIT AND AN INTERFACE ARE BOTH CONTRACTS HERE, AND THE TRAIT HALF IS WHAT
// MAKES THE TRAIT ENTRIES USEFUL. A php trait supplies members to the classes
// that use it, so a call landing on a trait member wants those classes one hop
// away — exactly what an interface's implementers get. Because the emission
// gate reads the RESOLVED target's contract flag, a trait declaration that left
// the flag false would capture every `use` entry and then emit nothing for a
// single one of them.
//
// THE PROPERTY MAP IS KEYED WITHOUT THE "$" SIGIL, deliberately and in contrast
// to the qualifier map: `$this->f` accesses member `f`, so a field map keyed
// `$f` would never be hit. The two spellings differ on purpose and neither is
// normalized into the other.
func phpTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	switch chunkType {
	case "class_declaration", "interface_declaration", "trait_declaration":
	default:
		return nil
	}

	facts := &TypeFacts{}
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		switch phpKinds().class(child.Symbol()) {
		case phpKindBaseClause:
			facts.Conforms = phpAppendNames(facts.Conforms, child, ConformExtends, src)
		case phpKindClassInterfaceClause:
			facts.Conforms = phpAppendNames(facts.Conforms, child, ConformImplements, src)
		case phpKindDeclarationList:
			facts.Conforms = phpAppendTraitUses(facts.Conforms, child, src)
			facts.Fields = phpPropertyTypes(child, src)
		}
	}
	facts.IsInterface = chunkType == "interface_declaration" || chunkType == "trait_declaration"
	return nominalTypeFacts(facts)
}

// phpAppendNames captures every DIRECT name child of a conformance clause.
func phpAppendNames(out []DeclaredSupertype, clause *sitter.Node, kind ConformanceKind, src []byte) []DeclaredSupertype {
	for i := range int(clause.NamedChildCount()) {
		child := clause.NamedChild(i)
		switch phpKinds().class(child.Symbol()) {
		case phpKindName, phpKindQualifiedName:
			out = nominalDeclaredSupertypes(out, child.Content(src), kind)
		}
	}
	return out
}

// phpAppendTraitUses captures the traits a class body pulls in with `use`.
//
// ONLY THE DIRECT name CHILDREN ARE TAKEN, AND THE WALK DOES NOT DESCEND. The
// conflict-resolution form `use A, B { A::go insteadof B; }` carries a use_list
// child whose nested names are ADAPTATIONS — a member being redirected — rather
// than supertypes, so a descending walk would record the METHOD NAME `go` as a
// declared supertype spelling.
func phpAppendTraitUses(out []DeclaredSupertype, body *sitter.Node, src []byte) []DeclaredSupertype {
	for i := range int(body.NamedChildCount()) {
		use := body.NamedChild(i)
		if phpKinds().class(use.Symbol()) != phpKindUseDeclaration {
			continue
		}
		out = phpAppendNames(out, use, ConformTrait, src)
	}
	return out
}

// phpPropertyTypes maps a declaration's property names — WITHOUT the sigil — to
// their declared types.
func phpPropertyTypes(body *sitter.Node, src []byte) map[string]string {
	var out map[string]string
	for i := range int(body.NamedChildCount()) {
		prop := body.NamedChild(i)
		if phpKinds().class(prop.Symbol()) != phpKindPropertyDeclaration {
			continue
		}
		text, names := phpPropertyParts(prop, src)
		if text == "" {
			continue
		}
		for _, name := range names {
			if out == nil {
				out = map[string]string{}
			}
			out[name] = text
		}
	}
	return out
}

// phpPropertyParts returns one property declaration's rendered type and the
// member names it declares, each with the "$" sigil REMOVED.
func phpPropertyParts(prop *sitter.Node, src []byte) (string, []string) {
	var typeNode *sitter.Node
	var names []string
	for i := range int(prop.NamedChildCount()) {
		child := prop.NamedChild(i)
		switch phpKinds().class(child.Symbol()) {
		case phpKindNamedType:
			if typeNode == nil {
				typeNode = child
			}
		case phpKindPropertyElement:
			// A variable_name's own `name` child is the member spelling with the
			// sigil already off — the sigil is a separate anonymous token — so
			// reading it is what produces the accessed spelling rather than
			// trimming the declared one.
			for j := range int(child.NamedChildCount()) {
				varName := child.NamedChild(j)
				if phpKinds().class(varName.Symbol()) != phpKindVariableName {
					continue
				}
				for k := range int(varName.NamedChildCount()) {
					inner := varName.NamedChild(k)
					if phpKinds().class(inner.Symbol()) == phpKindName {
						names = append(names, inner.Content(src))
						break
					}
				}
			}
		}
	}
	return phpQualTypeText(typeNode, src), names
}
