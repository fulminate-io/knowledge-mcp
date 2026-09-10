// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_types_test.go — THE ENUMERATION RUNS ON THE TYPE
// CHECKER, WHICH IS WHERE THE SPELLINGS RUN OUT.
//
// WHY NOT ANOTHER SYNTACTIC WALK. Four instruments answered "what is an error
// value" with a spelling — a hand list, a derivation off one code path, a walk
// over return statements, then a two-name constructor whitelist — and each was
// beaten by a construct Go already admits: a package-local constructor, a
// composite literal of a local error type, one sentinel returned from two sites,
// a second import alias of fmt. Every one of those is an ordinary line, and a
// refusal written that way could be added or deleted with the whole suite green.
// There is no list of spellings that ends, so the question is asked of go/types
// instead: an expression is a refusal because of the RESULT IT FILLS, not
// because of how it is written.
//
// THE DETECTION RULE, and why the obvious one is wrong. "An expression whose
// type is error" does NOT find a typed error: when the operand is a composite
// literal of a named type declared in the package, go/types records the
// operand's type as that named type, and the widening to error is implicit in
// the assignment to the result. So the rule is POSITIONAL. Match a
// return statement's operands against the enclosing signature's results
// (unpacking a tuple-valued single operand), and take every operand filling a
// result that IMPLEMENTS error and is not nil — implementing, not spelled
// `error`, because a result typed as an interface embedding error is filled by
// refusals exactly as a plain one is.
//
// AND THE WRITES INTO A NAMED RESULT, WHICH ARE ENUMERATED BY STATEMENT FORM.
// An assignment whose left-hand side names the result is keyed by its right-hand
// side, which is how a deferred func literal refuses. Every OTHER statement that
// can write such a result is REFUSED BY LINE, because it carries no single
// construction to key: a range assignment into the result, the same range inside
// a deferred literal, and a write through a pointer or through a slice or map
// holding the result's address. That refusal is the honest answer where a silence
// would be an arm deletable with the suite green.
//
// WHICH EXPRESSIONS ARE REFUSALS IS DECIDED HERE; WHAT EACH IS CALLED is decided
// in check_daemon_route_census_keys_test.go, which also lists what it refuses by
// line rather than dropping. A refusal is a red, never a skip: a skipped
// expression is an arm that can be deleted with the suite green, which is the
// whole defect this census exists to prevent.
//
// WHAT IT KEYS RATHER THAN REFUSES, and why that is a strengthening. This file
// hands back six errors it did not construct — three tuple relays of its own
// functions, the error of http.NewRequestWithContext, the tuple of Client.Do,
// and the dialer's DialContext. Refusing those by line would leave the census
// permanently red on correct code. Each is instead KEYED BY THE CALL IT CAME
// FROM and gets a row of its own, so six pass-throughs that were invisible to
// every earlier instrument now each name the test that observes them.
//
// ENUMERATION IS PER SITE, NOT PER CONSTRUCTION. The earlier census counted
// constructions, so one sentinel returned from two places was one row and the
// second place was observed by nothing. Here each site is its own candidate, two
// sites carrying one construction collide, and a collision is already a refusal.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// The construction kinds a refusal site can be keyed by. They are reported in
// complaints so a reader is told what the census thinks it is looking at.
const (
	constructionMessage   = "a message constructor"
	constructionSentinel  = "a package-level sentinel"
	constructionTypedLit  = "a composite literal of an error type"
	constructionLocalCall = "a call into this package"
	constructionRelay     = "a call outside this package"
)

// The refusals, spelled once so a test can name the one it expects. Each is
// prefixed with "<file>:<line> in <function> " by the walk.
const (
	censusNonStaticMessage = "builds an error with %s whose message argument is not a static string literal (%s) — " +
		"the census cannot key an arm it cannot read, and it refuses rather than dropping it. Write the message as a literal."
	censusUnclassifiedConstructor = "calls %s, an error constructor this census does not classify — " +
		"it cannot key the arm, so it would drop it, and a dropped arm is one that can be deleted with the suite green. " +
		"Build the refusal with fmt.Errorf or errors.New, or teach the census this shape."
	censusFromParameter = "hands back %s, which flows in from a parameter of %s — the construction is outside this file, " +
		"so the census cannot key it. Wrap it in a refusal this file constructs."
	censusNoConstruction    = "hands back %s, which this walk cannot see assigned anywhere in %s"
	censusManyConstructions = "hands back %s, which %s assigns in %d places, so the census cannot say which construction reaches here"
	censusUntraceable       = "hands back %s, which the census cannot trace to a construction"
	censusUnnamedCallee     = "hands back the result of %s, whose callee the census cannot name"
	censusRangedWrite       = "writes an element of %s into the named error result %s, and a ranged element has no single construction " +
		"the census can key. Assign the refusal to the result instead."
	censusIndirectWrite = "writes into %s, an error this walk cannot resolve to a name — a write through a pointer, or through an alias " +
		"holding a result's address, can reach a named error result of %s that the census cannot follow, so it refuses the write by line. " +
		"Assign the refusal to the named result directly."
)

// censusArm is one refusal SITE as the type-directed enumeration found it.
type censusArm struct {
	// key is the arm's identity and the text a row declares. For a message
	// constructor it is the format string exactly as the source spells it; for
	// every other construction it is a sentence naming what built the value.
	key string
	// kind names how the value was built, for the complaint text.
	kind string
	// construction renders the expression as the source spells it.
	construction string
	line         int
	// fn names where the site sits: a function, a func literal, or package level.
	fn string
	// guard is DERIVED, not declared: whether this site carries the error of a
	// json.Marshal, and whose value that Marshal encoded.
	guard marshalGuard
}

// enumerateRefusals is what the census counts with: every error-typed refusal
// site in routeSourceFile, plus the ones it refused to key. src stands in for
// the file on disk when it is non-nil, which is how the shape table points the
// census at a mutated copy without writing one.
func enumerateRefusals(src []byte) ([]censusArm, []string, error) {
	cp, err := loadCensusPackage()
	if err != nil {
		return nil, nil, err
	}
	route, info, pkg, err := cp.check(src)
	if err != nil {
		return nil, nil, err
	}
	body := src
	if body == nil {
		body = cp.routeSrc
	}

	w := &refusalWalk{
		fset:    cp.fset,
		info:    info,
		pkg:     pkg,
		src:     body,
		base:    routeSourceFile,
		results: namedErrorResults(route, info),
		guards:  marshalGuards(route, info, cp.fset, body),
	}
	w.walk(route)
	return w.arms, w.refused, nil
}

// namedErrorResults is the set of named result objects of type error declared
// anywhere in the file, func literals included. An assignment to one of them is
// a refusal even when the return that carries it is naked, which is how a
// deferred func literal sets its caller's error.
func namedErrorResults(file *ast.File, info *types.Info) map[types.Object]bool {
	results := map[types.Object]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		var typ *ast.FuncType
		switch fn := n.(type) {
		case *ast.FuncDecl:
			typ = fn.Type
		case *ast.FuncLit:
			typ = fn.Type
		default:
			return true
		}
		if typ.Results == nil {
			return true
		}
		for _, field := range typ.Results.List {
			for _, name := range field.Names {
				obj := info.ObjectOf(name)
				if obj != nil && isErrorType(obj.Type()) {
					results[obj] = true
				}
			}
		}
		return true
	})
	return results
}

// isErrorType reports whether a value of type t IS an error, by implementing the
// predeclared interface rather than by being spelled as it.
//
// AN IDENTITY TEST WAS THE LAST SPELLING. types.Identical against the
// predeclared error admits only a result written `error`, so a helper declaring
// `(n int) probeFailure`, where probeFailure is an interface embedding error, was
// no refusal site at all: an arm inside it could be added and deleted with the
// census green. A defined type whose underlying type is that interface, a named
// interface embedding it, and a concrete type carrying an Error method are all
// results a refusal fills, and implementing subsumes identity, so this predicate
// answers all of them and the earlier one at once. The pointer arm is the
// ordinary Go shape where Error is declared on the pointer receiver.
func isErrorType(t types.Type) bool {
	if t == nil {
		return false
	}
	iface, ok := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	return types.Implements(t, iface) || types.Implements(types.NewPointer(t), iface)
}

// censusFrame is one entry of the enclosing-function stack: the name a
// complaint reports, the signature a return's operands are matched against, and
// the body an identifier's construction is looked for in.
type censusFrame struct {
	name  string
	sig   *types.Signature
	body  *ast.BlockStmt
	depth int
}

// refusalWalk carries the enumeration's state across one file.
type refusalWalk struct {
	fset    *token.FileSet
	info    *types.Info
	pkg     *types.Package
	src     []byte
	base    string
	results map[types.Object]bool
	guards  map[token.Pos]marshalGuard

	stack   []censusFrame
	arms    []censusArm
	refused []string
}

// walk visits the file, tracking the enclosing function so every refusal site
// can be matched against a signature and named in a complaint.
func (w *refusalWalk) walk(file *ast.File) {
	depth := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			depth--
			for len(w.stack) > 0 && w.stack[len(w.stack)-1].depth > depth {
				w.stack = w.stack[:len(w.stack)-1]
			}
			return false
		}
		depth++
		switch node := n.(type) {
		case *ast.FuncDecl:
			w.push(node.Name.Name, w.signatureOf(node.Name), node.Body, depth)
		case *ast.FuncLit:
			w.push(w.where()+" (func literal)", w.signatureOf(node), node.Body, depth)
		case *ast.ReturnStmt:
			w.visitReturn(node)
		case *ast.AssignStmt:
			w.visitAssign(node)
		case *ast.RangeStmt:
			w.visitRange(node)
		}
		return true
	})
}

func (w *refusalWalk) push(name string, sig *types.Signature, body *ast.BlockStmt, depth int) {
	w.stack = append(w.stack, censusFrame{name: name, sig: sig, body: body, depth: depth})
}

// signatureOf reads the signature go/types gave a function declaration's name or
// a func literal.
func (w *refusalWalk) signatureOf(node ast.Node) *types.Signature {
	switch n := node.(type) {
	case *ast.Ident:
		if obj := w.info.ObjectOf(n); obj != nil {
			if sig, ok := obj.Type().(*types.Signature); ok {
				return sig
			}
		}
	case ast.Expr:
		if tv, ok := w.info.Types[n]; ok {
			if sig, ok := tv.Type.(*types.Signature); ok {
				return sig
			}
		}
	}
	return nil
}

// frame is the innermost enclosing function.
func (w *refusalWalk) frame() censusFrame {
	if len(w.stack) == 0 {
		return censusFrame{name: "package level"}
	}
	return w.stack[len(w.stack)-1]
}

func (w *refusalWalk) where() string { return w.frame().name }

// visitReturn takes every operand filling an error-typed result.
func (w *refusalWalk) visitReturn(ret *ast.ReturnStmt) {
	fr := w.frame()
	if fr.sig == nil || fr.sig.Results().Len() == 0 || len(ret.Results) == 0 {
		return
	}
	// `return f()` where f returns several values: the one operand carries a
	// tuple, and the error component came from the callee.
	if len(ret.Results) == 1 && fr.sig.Results().Len() > 1 {
		if tuple, ok := w.info.TypeOf(ret.Results[0]).(*types.Tuple); ok {
			for result := range tuple.Variables() {
				if isErrorType(result.Type()) {
					w.candidate(ret.Results[0], fr)
					return
				}
			}
			return
		}
	}
	for i, operand := range ret.Results {
		if i >= fr.sig.Results().Len() {
			return
		}
		if isErrorType(fr.sig.Results().At(i).Type()) {
			w.candidate(operand, fr)
		}
	}
}

// visitAssign takes the value an assignment writes into a named error result,
// and refuses by line an assignment whose target is an error this walk cannot
// resolve to a name.
func (w *refusalWalk) visitAssign(assign *ast.AssignStmt) {
	fr := w.frame()
	for i, lhs := range assign.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			w.refuseIndirectWrite(lhs, fr)
			continue
		}
		if !w.results[w.info.ObjectOf(ident)] {
			continue
		}
		switch {
		case len(assign.Rhs) == len(assign.Lhs):
			w.candidate(assign.Rhs[i], fr)
		case len(assign.Rhs) == 1:
			w.candidate(assign.Rhs[0], fr)
		}
	}
}

// visitRange takes the range statements that write a named error result. A range
// writes an ELEMENT of the ranged value, and an element has no single expression
// the census could key the write by, so it is REFUSED BY LINE rather than passed
// over: `for _, err = range errs {}` in a function with a named error result, and
// the same statement inside a deferred literal, both set the caller's error while
// no assignment statement carries it.
func (w *refusalWalk) visitRange(rng *ast.RangeStmt) {
	// NOTHING HERE REJECTS A `:=` RANGE, and it does not need to: that form
	// declares FRESH variables, whose objects are not the ones namedErrorResults
	// collected, so the membership test below already excludes them. A `rng.Tok !=
	// token.ASSIGN` guard was written first and deleted when killing it left the
	// whole suite green; an unobserved guard reads as protection and is not.
	fr := w.frame()
	for _, target := range []ast.Expr{rng.Key, rng.Value} {
		if target == nil {
			continue
		}
		ident, ok := target.(*ast.Ident)
		if !ok {
			w.refuseIndirectWrite(target, fr)
			continue
		}
		if !w.results[w.info.ObjectOf(ident)] {
			continue
		}
		w.refuse(target, fr, fmt.Sprintf(censusRangedWrite, w.render(rng.X), ident.Name))
	}
}

// refuseIndirectWrite refuses, by line, a write whose target is an error value
// this walk cannot resolve to a name: `*p = ...` through a pointer taken of a
// result, and `*alias[0] = ...` through a slice or map holding that address. Once
// the address is in a name, which result a write reaches is not decidable on the
// code alone — the pointer can be handed to another function — so the census
// says so by line instead of going quiet, and a quiet write is exactly the arm
// that can be deleted with the suite green.
//
// It fires only inside a function that DECLARES a named error result, because
// that is the only place such a write can reach one; a pointer write in any other
// function is not this census's business.
func (w *refusalWalk) refuseIndirectWrite(lhs ast.Expr, fr censusFrame) {
	if !w.inNamedErrorResultScope() || !isErrorType(w.info.TypeOf(lhs)) {
		return
	}
	w.refuse(lhs, fr, fmt.Sprintf(censusIndirectWrite, w.render(lhs), fr.name))
}

// inNamedErrorResultScope reports whether any enclosing function declares a named
// error result. The whole stack is read, not the innermost frame, because a
// deferred func literal writes the result of the function it was deferred in.
func (w *refusalWalk) inNamedErrorResultScope() bool {
	for _, fr := range w.stack {
		if fr.sig == nil {
			continue
		}
		for result := range fr.sig.Results().Variables() {
			if result.Name() != "" && isErrorType(result.Type()) {
				return true
			}
		}
	}
	return false
}

// refuse records one refusal against the line of expr.
func (w *refusalWalk) refuse(expr ast.Expr, fr censusFrame, refusal string) {
	w.refused = append(w.refused, fmt.Sprintf("%s:%d in %s %s", w.base, w.fset.Position(expr.Pos()).Line, fr.name, refusal))
}

// candidate classifies one refusal site into an arm or a refusal.
func (w *refusalWalk) candidate(expr ast.Expr, fr censusFrame) {
	if tv, ok := w.info.Types[expr]; ok && tv.IsNil() {
		return
	}
	line := w.fset.Position(expr.Pos()).Line
	key, kind, refusal := w.classify(expr, fr, 0)
	if refusal != "" {
		w.refused = append(w.refused, fmt.Sprintf("%s:%d in %s %s", w.base, line, fr.name, refusal))
		return
	}
	w.arms = append(w.arms, censusArm{
		key:          key,
		kind:         kind,
		construction: w.render(expr),
		line:         line,
		fn:           fr.name,
		guard:        w.guards[expr.Pos()],
	})
}
