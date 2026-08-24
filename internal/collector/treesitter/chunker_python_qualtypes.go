// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterPythonQualifierTypes()
}

// The python kind classes. pyKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name.
const (
	pyKindOther uint8 = iota
	pyKindClassDefinition
	pyKindFunctionDefinition
	pyKindParameters
	pyKindTypedParameter
	pyKindTypedDefaultParameter
	pyKindArgumentList
	pyKindKeywordArgument
	pyKindBlock
	pyKindExpressionStatement
	pyKindAssignment
	pyKindType
	pyKindIdentifier
	pyKindAttribute
	pyKindCall
	pyKindString
	pyKindGenericType
	pyKindSubscript
	pyKindDecoratedDefinition
	pyKindDecorator
)

// pyKindNames maps every python node-kind spelling the python arms name onto
// its class code.
//
// EVERY NAME WAS ENUMERATED AGAINST THE VENDORED GRAMMAR rather than read off a
// reference, because newSymbolClasses panics for a name the grammar declares no
// REGULAR symbol under. Two results of that enumeration are worth recording:
// python's `class` and `def` keywords are ANONYMOUS and so are absent here by
// necessity rather than by choice, while `type` — which is both a soft keyword
// and a node kind — does declare a regular symbol and is safe to name. `block`
// carries TWO regular ids, the multiplicity newSymbolClasses assigns every
// member of rather than stopping at the first.
var pyKindNames = map[string]uint8{
	"class_definition":        pyKindClassDefinition,
	"function_definition":     pyKindFunctionDefinition,
	"parameters":              pyKindParameters,
	"typed_parameter":         pyKindTypedParameter,
	"typed_default_parameter": pyKindTypedDefaultParameter,
	"argument_list":           pyKindArgumentList,
	"keyword_argument":        pyKindKeywordArgument,
	"block":                   pyKindBlock,
	"expression_statement":    pyKindExpressionStatement,
	"assignment":              pyKindAssignment,
	"type":                    pyKindType,
	"identifier":              pyKindIdentifier,
	"attribute":               pyKindAttribute,
	"call":                    pyKindCall,
	"string":                  pyKindString,
	"generic_type":            pyKindGenericType,
	"subscript":               pyKindSubscript,
	"decorated_definition":    pyKindDecoratedDefinition,
	"decorator":               pyKindDecorator,
}

// pyKindTable is the python instance of the shared memo.
var pyKindTable = kindTable{lang: LangPython, names: pyKindNames}

// pyKinds returns the memoized python symbol class table.
func pyKinds() symbolClasses { return pyKindTable.get() }

// RegisterPythonQualifierTypes installs the python qualifier-type arm, exported
// for the restore-not-delete reason RegisterGoQualifierTypes states.
func RegisterPythonQualifierTypes() {
	RegisterQualifierTypes(LangPython, pythonQualifierTypes)
}

// pythonQualifierTypes is the python arm: one walk of a declaration's subtree,
// returning the qualifier names it makes visible mapped to their declared types.
//
// NO EXPORT UNWRAP IS NEEDED, and the reason is structural rather than an
// oversight: python has no export statement, and a DECORATED definition produces
// a separate unnamed decorated_definition chunk alongside the named inner one —
// so an arm keyed on the class or function chunk always receives the definition
// node itself, decorated or not.
func pythonQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	b := &qualBinder{classes: pyKinds()}
	bindPythonReceiver(b, declNode, src)
	walkPythonQualifiers(b, declNode, src)

	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// bindPythonReceiver binds a method's receiver parameter to its class.
//
// THE NAME IS TAKEN AS WRITTEN RATHER THAN MATCHED AGAINST `self`. A
// classmethod's receiver is spelled `cls`, and a project that spells it
// something else entirely is still writing a receiver — matching the
// conventional spelling would bind the first case, miss the second, and call the
// third a style violation it has no business having an opinion about.
//
// AN ANNOTATED FIRST PARAMETER IS NOT A RECEIVER. It carries its own declared
// type, which the parameter route reads; treating it as the receiver would
// overwrite a stated type with an inferred one.
func bindPythonReceiver(b *qualBinder, decl *sitter.Node, src []byte) {
	if b.classes.class(decl.Symbol()) != pyKindFunctionDefinition {
		return
	}
	// A STATICMETHOD HAS NO RECEIVER, so its first parameter is an ORDINARY
	// ARGUMENT and binding it to the class states a type the value does not
	// have. This is the one decorator that changes the meaning of the parameter
	// list rather than wrapping the function, which is why it is read here and
	// no other decorator is: @classmethod still takes a receiver (spelled cls),
	// and @property, @cached_property and every user-written decorator leave the
	// first parameter a receiver too.
	if pythonIsStaticMethod(b.classes, decl, src) {
		return
	}
	class := pythonEnclosingClassName(decl, src)
	if class == "" {
		return
	}
	params := pythonFirstChildOfClass(b.classes, decl, pyKindParameters)
	if params == nil || params.NamedChildCount() == 0 {
		return
	}
	first := params.NamedChild(0)
	if b.classes.class(first.Symbol()) != pyKindIdentifier {
		return
	}
	b.bind(first.Content(src), QualType{Text: class})
}

// pythonIsStaticMethod reports whether a function definition carries the
// @staticmethod decorator.
//
// THE DECORATORS ARE THE PARENT'S, NOT THE FUNCTION'S. A decorated definition
// wraps the function_definition in a decorated_definition node holding the
// decorator children, so the chunk's own node carries none of them and a scan of
// its children would find nothing and silently report every method undecorated.
//
// The name is compared BARE, after the sigil: `@staticmethod` is the only form
// that matters here, and a dotted or called decorator spelling is deliberately
// not matched rather than guessed at.
func pythonIsStaticMethod(classes symbolClasses, decl *sitter.Node, src []byte) bool {
	parent := decl.Parent()
	if parent == nil || classes.class(parent.Symbol()) != pyKindDecoratedDefinition {
		return false
	}
	for i := range int(parent.NamedChildCount()) {
		child := parent.NamedChild(i)
		if classes.class(child.Symbol()) != pyKindDecorator {
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(child.Content(src), "@")) == "staticmethod" {
			return true
		}
	}
	return false
}

// pythonEnclosingClassName returns the name of the nearest class the
// declaration sits in, or "" when it sits in none.
//
// The ascent stops at the first class-like ancestor whatever its name, for the
// reason the ECMAScript ascent does: walking past an unnamed one would bind the
// receiver to an OUTER class, naming a type the value does not have.
func pythonEnclosingClassName(decl *sitter.Node, src []byte) string {
	// One language per file, so the admission row is a constant here, read
	// once rather than per ancestor hop.
	admit := classLikeByLang[LangPython]
	for p := decl.Parent(); p != nil; p = p.Parent() {
		if admit[p.Type()] {
			if p.Type() != "class_definition" {
				return ""
			}
			return containerName(p, src)
		}
	}
	return ""
}

// walkPythonQualifiers descends one declaration binding the local syntax that
// makes a qualifier visible: annotated parameters in either form, annotated
// assignments, and constructor calls.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL rather than the kind name, for the
// allocation reason the Go arm's walk documents: reading a node's kind name
// converts a cgo C-string into a fresh Go string on every call.
func walkPythonQualifiers(b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case pyKindTypedParameter, pyKindTypedDefaultParameter:
			// BOTH FORMS, and the second is not a variant of the first: a
			// defaulted annotated parameter `def f(x: Foo = None)` is its own
			// regular symbol, so an arm naming only typed_parameter misses every
			// parameter that carries a default — which in real python is most of
			// the optional ones.
			bindPythonAnnotated(b, child, src)
		case pyKindAssignment:
			bindPythonAssignment(b, child, src)
		}
		walkPythonQualifiers(b, child, src)
	}
}

// bindPythonAnnotated binds one annotated parameter: its identifier to the type
// its `type` child names.
func bindPythonAnnotated(b *qualBinder, param *sitter.Node, src []byte) {
	name := pythonFirstChildOfClass(b.classes, param, pyKindIdentifier)
	typeNode := pythonFirstChildOfClass(b.classes, param, pyKindType)
	if name == nil || typeNode == nil {
		return
	}
	b.bind(name.Content(src), QualType{Text: pythonTypeText(b.classes, typeNode, src)})
}

// bindPythonAssignment binds an assignment, by annotation first and by
// constructor call second.
//
// THE ANNOTATION WINS, as an if/else rather than two binds: qualBinder treats a
// rebind to different text as a conflict and DELETES the entry, so binding both
// halves of `v: Sink = Impl()` would knock the qualifier out altogether. The
// written annotation is the declared type; the initialiser is evidence of it.
func bindPythonAssignment(b *qualBinder, assign *sitter.Node, src []byte) {
	if assign.NamedChildCount() == 0 {
		return
	}
	name := assign.NamedChild(0)
	if b.classes.class(name.Symbol()) != pyKindIdentifier {
		// A destructuring or attribute target names no plain qualifier.
		return
	}
	if typeNode := pythonFirstChildOfClass(b.classes, assign, pyKindType); typeNode != nil {
		b.bind(name.Content(src), QualType{Text: pythonTypeText(b.classes, typeNode, src)})
		return
	}
	value := assign.ChildByFieldName("right")
	if value == nil || b.classes.class(value.Symbol()) != pyKindCall {
		return
	}
	b.bind(name.Content(src), QualType{Text: pythonConstructorText(b.classes, value, src)})
}

// pythonConstructorText returns the type a construction names, or "" to decline.
//
// IN PYTHON THE CALLEE OF A CONSTRUCTION IS THE CLASS, so `x = Client()` records
// Client as a DIRECT type rather than going through the call-return route.
//
// IT IS BIND-ONLY SAFE EVEN WHEN THE CALLEE IS A FUNCTION, which is the obvious
// objection and is answered by the rung rather than by a check here: if `Client`
// names a function, the qualifier resolves to that function's declaration and the
// member lookup for {Parent: Client, Name: method} then finds nothing, because a
// function declares no members — so the rung declines and emits no edge. The
// residual is a scope declaring BOTH a class and a function of one name where the
// class declares the called member, which is not a shape python code produces.
//
// A DOTTED CALLEE DECLINES: `x = mod.Client()` has an attribute callee whose
// qualifier is itself a bound name, and resolving that is a hop this arm does not
// take.
func pythonConstructorText(classes symbolClasses, call *sitter.Node, src []byte) string {
	if call.NamedChildCount() == 0 {
		return ""
	}
	callee := call.NamedChild(0)
	if classes.class(callee.Symbol()) != pyKindIdentifier {
		return ""
	}
	return callee.Content(src)
}

// pythonTypeText renders the type a `type` node names, or "" to decline it.
//
// IT IS A CLOSED ALLOWLIST accepting a bare name and a dotted attribute and
// declining everything else. The three declines that matter are all shapes real
// python writes constantly: a STRING forward reference (`x: "Bar"`) parses as a
// string rather than a name; a SUBSCRIPT (`list[Foo]`, `Optional[Foo]`) names a
// container whose methods the value does not have; and a generic_type is the same
// case one node kind up. Binding any of them would attribute a method to a value
// that cannot receive it, which is the wrong-target class this rung's bind-only
// bar forbids.
func pythonTypeText(classes symbolClasses, typeNode *sitter.Node, src []byte) string {
	if typeNode.NamedChildCount() == 0 {
		return ""
	}
	inner := typeNode.NamedChild(0)
	switch classes.class(inner.Symbol()) {
	case pyKindIdentifier, pyKindAttribute:
		return inner.Content(src)
	}
	return ""
}

// pythonFirstChildOfClass returns a node's first direct named child of one
// class, or nil.
func pythonFirstChildOfClass(classes symbolClasses, node *sitter.Node, class uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == class {
			return child
		}
	}
	return nil
}
