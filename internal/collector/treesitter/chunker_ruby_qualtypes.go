// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterRubyQualifierTypes()
}

// The ruby kind classes. rbKindOther is the ZERO VALUE and therefore the class
// of every symbol the table does not name.
const (
	rbKindOther uint8 = iota
	rbKindClass
	rbKindModule
	rbKindSuperclass
	rbKindConstant
	rbKindScopeResolution
	rbKindCall
	rbKindArgumentList
	rbKindBodyStatement
	rbKindMethod
	rbKindSelf
	rbKindAssignment
	rbKindIdentifier
	rbKindMethodParameters
	rbKindInstanceVariable
	rbKindReturn
)

// rbKindNames maps every ruby node-kind spelling the ruby arms name onto its
// class code.
//
// NAMING `class` AND `module` IS SAFE, AND THE REASON IS NOT OBVIOUS. Each is
// declared TWICE by this grammar — once as an anonymous keyword token and once
// as a regular node kind — and newSymbolClasses walks only REGULAR symbols, so
// the map binds the node kinds and the keyword tokens stay unclassified. The
// entries below are therefore about the declarations, never about the keywords.
//
// THE `<` SUPERCLASS OPERATOR IS ANONYMOUS-ONLY and is deliberately ABSENT: a
// kind map naming it would panic at first use. It is reached through the regular
// `superclass` wrapper instead, which is why that wrapper is named here.
var rbKindNames = map[string]uint8{
	"class":             rbKindClass,
	"module":            rbKindModule,
	"superclass":        rbKindSuperclass,
	"constant":          rbKindConstant,
	"scope_resolution":  rbKindScopeResolution,
	"call":              rbKindCall,
	"argument_list":     rbKindArgumentList,
	"body_statement":    rbKindBodyStatement,
	"method":            rbKindMethod,
	"self":              rbKindSelf,
	"assignment":        rbKindAssignment,
	"identifier":        rbKindIdentifier,
	"method_parameters": rbKindMethodParameters,
	"instance_variable": rbKindInstanceVariable,
	"return":            rbKindReturn,
}

// rbKindTable is the ruby instance of the shared memo.
var rbKindTable = kindTable{lang: LangRuby, names: rbKindNames}

// rbKinds returns the memoized ruby symbol class table.
func rbKinds() symbolClasses { return rbKindTable.get() }

// RegisterRubyQualifierTypes installs the ruby qualifier-type arm, exported for
// the restore-not-delete reason RegisterGoQualifierTypes states.
func RegisterRubyQualifierTypes() {
	RegisterQualifierTypes(LangRuby, rubyQualifierTypes)
}

// rubyQualifierTypes is the ruby arm.
//
// TWO ROUTES, AND THE LANGUAGE OFFERS NO THIRD. Ruby writes no type annotations
// at all, so there is no parameter route and no local-annotation route; and it
// declares no return types, so the call-return route has nothing to read and
// TypeFacts.Results stays empty for every ruby declaration. What remains is the
// receiver and the allocator, and they carry the whole arm.
func rubyQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	b := &qualBinder{classes: rbKinds()}
	if name := rubyContainerName(declNode, src); name != "" {
		b.bind("self", QualType{Text: name})
	}
	walkRubyQualifiers(b, declNode, src)

	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// rubyContainerName returns the container `self` refers to inside one
// declaration, or "" when there is none.
//
// A DECLARATION THAT IS ITSELF A CONTAINER IS ITS OWN ANSWER, AND MUST NOT
// ASCEND. At a ruby class or module's BODY LEVEL, `self` IS that container —
// so a nested `class Inner` inside `class Outer` binds self to Inner, and an
// ascent starting at decl.Parent() would walk straight past Inner and bind it
// to Outer instead. That is a wrong target rather than a missing one: every
// body-level `self.x` in the inner container would be attributed to a type the
// receiver does not have.
//
// Only a declaration that is NOT a container ascends, and its ascent stops at
// the first container ancestor whatever its name — walking past an unnamed one
// would reach further out and make the same mistake one level up.
func rubyContainerName(decl *sitter.Node, src []byte) string {
	// This file registers exactly one language, so the admission row is a
	// constant here rather than a threaded parameter, and it is read once
	// rather than per ancestor hop.
	admit := classLikeByLang[LangRuby]
	if admit[decl.Type()] {
		return containerName(decl, src)
	}
	for p := decl.Parent(); p != nil; p = p.Parent() {
		if admit[p.Type()] {
			return containerName(p, src)
		}
	}
	return ""
}

// walkRubyQualifiers descends one declaration binding local allocator
// assignments.
func walkRubyQualifiers(b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		if b.classes.class(child.Symbol()) == rbKindAssignment {
			bindRubyAllocator(b, child, src)
		}
		walkRubyQualifiers(b, child, src)
	}
}

// bindRubyAllocator binds `x = Client.new` to the type Client.
//
// ONLY `.new`, AND THE NARROWNESS IS THE POINT. `new` is ruby's allocator, so
// the constant in front of it IS the type of the value. A general
// strip-the-last-segment rule would bind `x = Client.build` to Client as well,
// where the value's type is whatever build happens to return — a guess dressed
// as a fact, and exactly the wrong-target class this bind-only rung forbids.
func bindRubyAllocator(b *qualBinder, assign *sitter.Node, src []byte) {
	if assign.NamedChildCount() < 2 {
		return
	}
	name, value := assign.NamedChild(0), assign.NamedChild(1)
	if b.classes.class(name.Symbol()) != rbKindIdentifier {
		return
	}
	if b.classes.class(value.Symbol()) != rbKindCall || value.NamedChildCount() < 2 {
		return
	}
	receiver, method := value.NamedChild(0), value.NamedChild(1)
	if b.classes.class(receiver.Symbol()) != rbKindConstant {
		return
	}
	if b.classes.class(method.Symbol()) != rbKindIdentifier || method.Content(src) != "new" {
		return
	}
	b.bind(name.Content(src), QualType{Text: receiver.Content(src)})
}
