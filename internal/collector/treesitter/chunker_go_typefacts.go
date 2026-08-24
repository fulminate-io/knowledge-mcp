// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterGoTypeFacts()
}

// RegisterGoTypeFacts installs the Go type-facts arm, exported for the same
// restore-not-delete reason as RegisterGoQualifierTypes.
func RegisterGoTypeFacts() {
	RegisterTypeFacts(LangGo, goTypeFacts)
}

// goTypeFacts records a Go declaration's syntax-visible type facts: the
// declared result types of a function or method, and the field types of a
// struct type declaration.
//
// THE CLASS RULE: EVERY TYPE TEXT THAT WILL LATER BE RESOLVED GOES THROUGH
// goQualTypeText — one discipline, both paths. That is what makes `*Thing` and
// `Thing` reach the same declaration, strips `Box[int]` to `Box`, and turns a
// container like `[]Delta` into an HONEST DECLINE rather than a name that
// merely fails to match. Recording such a text verbatim looks harmless because
// resolution "just doesn't find it" — but a populated ref carrying an
// unmatchable name is indistinguishable from a real lookup miss, and there is
// no descent downstream that would strip it, because resolution consumes these
// texts rather than re-parsing them.
//
// THE TWO CARRIERS DECLINE DIFFERENTLY, and the asymmetry is deliberate.
// Results is POSITIONAL — ResultIndex indexes into it — so a declining result
// is stored as the EMPTY STRING to hold its slot; dropping it would shift every
// later result and silently rebind a multi-value assignment to the wrong type.
// Fields is keyed by NAME, so a declining field is OMITTED, an absent entry and
// an empty one meaning the same thing to a map reader.
// resolveTypeText's empty-text guard turns either into the zero typeRef.
func goTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	switch chunkType {
	case "function_declaration", "method_declaration":
		results := goDeclaredResults(declNode, src)
		// The composed signature is recorded even when the declaration has no
		// results, because a no-result method still has an identity to match
		// against an interface spec that also declares none.
		_, params, result := goSignatureParts(declNode)
		sig := goSigFacts(params, result, src)
		if len(results) == 0 && sig == nil {
			return nil
		}
		return &TypeFacts{Results: results, Sig: sig}
	case "method_elem":
		// A SPEC CARRIES ONLY ITS SIGNATURE. It declares no locals to bind and no
		// fields, and its results are not a call's value — they are half of its
		// identity, which Sig already holds.
		params, result := goMethodElemSigParts(declNode)
		sig := goSigFacts(params, result, src)
		if sig == nil {
			return nil
		}
		return &TypeFacts{Sig: sig}
	case "type_declaration":
		fields := goStructFieldTypes(declNode, src)
		embeds := goAllEmbeds(declNode, src)
		isIface := goDeclIsInterface(declNode)
		isGeneric := goDeclIsGeneric(declNode)
		if len(fields) == 0 && len(embeds) == 0 && !isIface && !isGeneric {
			return nil
		}
		return &TypeFacts{Fields: fields, Embeds: embeds, IsInterface: isIface, IsGeneric: isGeneric}
	}
	return nil
}

// goDeclIsInterface reports whether a type_declaration's spec declares an
// interface.
//
// IT READS THE type_spec's `type` FIELD AND NEVER SEARCHES DESCENDANTS. A
// depth-first search for an interface_type would report true for `type S struct
// { X interface{ T } }` and for `type I interface { F(x struct{...}) }` alike —
// the first is a struct and the second's nested body is not the declaration's
// own kind. The field read answers about THIS declaration only.
//
// A GROUPED DECLARATION DECLINES. `type ( A struct{...}; B interface{...} )`
// holds several type_specs under one type_declaration, and the emitting
// declaration node is shared, so an extractor given only the declaration cannot
// tell which spec it serves. Answering for the first spec would credit every
// spec in the group with the first one's kind.
func goDeclIsInterface(declNode *sitter.Node) bool {
	spec := goSoleTypeSpec(declNode)
	if spec == nil {
		return false
	}
	body := spec.ChildByFieldName("type")
	return body != nil && body.Type() == "interface_type"
}

// goDeclIsGeneric reports whether a type_declaration's spec carries type
// PARAMETERS — `type Box[T any] ...`.
//
// IT READS THE PARSE TREE BECAUSE NOTHING DOWNSTREAM CAN RECOVER THE ANSWER. A
// type parameter is an unqualified identifier, so once a signature is resolved
// it looks exactly like a same-package type, and the tempting inference —
// "resolves to a name the index declares nowhere" — is WRONG, because a type
// ALIAS is a real same-package type that the declaration query never captures.
// The type_parameter_list is a direct child of the spec and answers the question
// outright.
func goDeclIsGeneric(declNode *sitter.Node) bool {
	spec := goSoleTypeSpec(declNode)
	if spec == nil {
		return false
	}
	for i := range int(spec.NamedChildCount()) {
		if spec.NamedChild(i).Type() == "type_parameter_list" {
			return true
		}
	}
	return false
}

// goSoleTypeSpec returns a type_declaration's ONLY type_spec, or nil when it
// holds none or more than one.
//
// THE COUNT IS THE DECLINE RULE. More than one type_spec means a grouped
// declaration whose specs share the emitting node, and an honest "cannot
// attribute" is strictly better than a confident wrong answer. An ALIAS group
// (`type ( X = Y )`) holds type_alias nodes rather than type_specs, so it
// returns nil here for the simpler reason that it has no spec at all.
func goSoleTypeSpec(declNode *sitter.Node) *sitter.Node {
	if declNode == nil {
		return nil
	}
	var found *sitter.Node
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if child.Type() != "type_spec" {
			continue
		}
		if found != nil {
			return nil
		}
		found = child
	}
	return found
}

// goDeclaredResults returns a declaration's result types in source order.
//
// THE FOUR SHAPES, each confirmed against a real parse:
//  1. BARE TYPE — `func a() T`: the node after the parameter list is a single
//     type node. ONE result.
//  2. UNNAMED LIST — `func b() (T, error)`: that node is a parameter_list whose
//     parameter_declarations carry only a type. N results, in order.
//  3. NAMED LIST — `func c() (t T, err error)`: the same parameter_list shape
//     with identifiers before each type. Take the type, ignore the names, and
//     let one declaration holding two identifiers and one type contribute TWO
//     results — the same rule the parameter case uses.
//  4. COMPOSED BARE TYPE — `func d() []*pkg.T`: one result whose top kind is a
//     container. It still occupies ITS POSITION, but its text DECLINES to the
//     empty string, because a slice value has no methods to bind.
//
// Every shape's text is rendered by goQualTypeText, never by Content — see the
// class rule on goTypeFacts.
func goDeclaredResults(declNode *sitter.Node, src []byte) []string {
	classes := goKinds()
	_, _, result := goSignatureParts(declNode)
	if result == nil {
		return nil
	}
	if classes.class(result.Symbol()) != goKindParameterList {
		// Shapes 1 and 4: a single result, however composed.
		return []string{goQualTypeText(result, src)}
	}
	// Shapes 2 and 3.
	results := make([]string, 0, result.NamedChildCount())
	for i := range int(result.NamedChildCount()) {
		decl := result.NamedChild(i)
		if classes.class(decl.Symbol()) != goKindParameterDeclaration {
			continue
		}
		names, typeNode := goNamesAndType(decl, src)
		if typeNode == nil {
			// An unnamed result: the whole declaration IS the type.
			if decl.NamedChildCount() > 0 {
				results = append(results, goQualTypeText(decl.NamedChild(0), src))
			}
			continue
		}
		// A named result contributes one entry PER NAME, so the positions keep
		// lining up with the values an assignment receives.
		text := goQualTypeText(typeNode, src)
		for range names {
			results = append(results, text)
		}
	}
	return results
}

// goStructFieldTypes maps a struct type declaration's NAMED fields to their
// declared types, as written.
//
// It is the mirror image of extractGoEmbeds, which walks the same
// field_declaration_list and keeps the branch this one skips: an embedded field
// is field_declaration(<type>) with no name, while a named field is
// field_declaration(field_identifier, <type>). One field_declaration may carry
// several names sharing one type, so every name is recorded.
func goStructFieldTypes(declNode *sitter.Node, src []byte) map[string]string {
	classes := goKinds()
	// findNodeByType is the shared Go helper at chunker_go.go:140, used by
	// every Go arm and by the chunker itself, so it stays on its string
	// argument: it is OUTSIDE this ticket's two-file fence, and both benchmark
	// sides pay for it equally. goKindStructType and goKindFieldDeclarationList
	// classify these same two kinds for the day it is converted with its own
	// caller census.
	structNode := findNodeByType(declNode, "struct_type")
	if structNode == nil {
		return nil
	}
	fieldList := findNodeByType(structNode, "field_declaration_list")
	if fieldList == nil {
		return nil
	}

	fields := make(map[string]string, fieldList.NamedChildCount())
	for i := range int(fieldList.NamedChildCount()) {
		field := fieldList.NamedChild(i)
		if classes.class(field.Symbol()) != goKindFieldDeclaration {
			continue
		}
		var names []*sitter.Node
		var typeNode *sitter.Node
		for j := range int(field.NamedChildCount()) {
			child := field.NamedChild(j)
			kind := classes.class(child.Symbol())
			if kind == goKindFieldIdentifier {
				names = append(names, child)
				continue
			}
			// The first non-name, non-tag node is the type.
			if typeNode == nil && kind != goKindInterpretedStringLiteral &&
				kind != goKindRawStringLiteral {
				typeNode = child
			}
		}
		if typeNode == nil || len(names) == 0 {
			// No names means an embedded field, which extractGoEmbeds owns.
			continue
		}
		// A field whose type DECLINES is OMITTED rather than stored empty:
		// this map is keyed by name, so an absent entry and an empty one mean
		// the same thing to every reader. Results cannot do this — they are
		// positional — which is the one asymmetry between the two carriers.
		text := goQualTypeText(typeNode, src)
		if text == "" {
			continue
		}
		for _, name := range names {
			fields[name.Content(src)] = text
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}
