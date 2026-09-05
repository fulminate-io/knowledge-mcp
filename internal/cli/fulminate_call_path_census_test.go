// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// THE CENSUS GATE. Every call this binary makes to a Fulminate service must
// carry the client version, and the version gate is a SECURITY blocking
// capability — one ungated path defeats a block. So coverage here is
// census-grade: the deliverable is not "the paths we know of" but an
// enumeration with a per-path disposition and a gate that goes red when the
// population changes.
//
// The property is SET EQUALITY between the construction sites that exist and
// the sites the manifest dispositions — not a single AST shape, which is why
// this is a census test rather than a corpus check.

// dispositions. The vocabulary is FOUR values, not three.
//
// The first three make a claim about the SITE ITSELF, verifiable by reading
// that one site. The fourth makes a REFERENTIAL claim about another row, which
// is why the manifest carries a reaches field and why this file checks it: of
// the four it is the only one whose truth a set-equality gate can check for
// free, because the target set is already the thing the gate holds.
const (
	dispStampedHere      = "stamped-at-this-site"
	dispStampedTransport = "stamped-by-its-transport"
	dispReaches          = "reaches-a-dispositioned-site"
	dispExcluded         = "excluded-with-reason"
)

// censusRow is the manifest schema: {file, symbol, disposition, reaches}.
//
// File is the path relative to cmd/knowledge. Symbol is the callee expression
// text plus a 1-based occurrence ordinal within that file — deliberately NOT a
// line number, because a line number rots on every edit above it and would turn
// this gate red on changes that touch no call path.
//
// Reaches is REQUIRED AND NON-EMPTY exactly when Disposition is dispReaches,
// and MUST be empty otherwise. Both directions are checked: a stray referent on
// an excluded row is as much a mislabel as a missing one on a referring row.
type censusRow struct {
	File        string
	Symbol      string
	Disposition string
	Reaches     string
}

// key is the manifest's identity for a site.
func (r censusRow) key() string { return r.File + ":" + r.Symbol }

// callShapes is the named set of call shapes the walk recognizes, kept as a
// readable list so it can be compared against the census the plan recorded
// rather than buried in a substring match.
//
// TWO NARROWINGS WERE REMOVED AND MUST NOT COME BACK, and they are the same
// defect at different levels:
//
//   - NO PACKAGE ALLOWLIST. The walk covers ALL of cmd/knowledge. An earlier
//     form walked six named packages and so could not see internal/bootstrap —
//     which already builds outbound HTTP and is the single likeliest home for a
//     new Fulminate-bound call.
//   - NO GAP IN THE DIAL VOCABULARY. net.DialTimeout is a package FUNCTION, not
//     a method on a dialer, so a dialer-method-only shape set missed it
//     entirely.
//
// requestClone is the third member of that family and was added for the same
// reason: the connect transport does not CONSTRUCT its outbound request, it
// CLONES one, so a construction-only vocabulary could not see one of the two
// chokepoints this whole feature is built on. It is discriminated structurally
// rather than by name — http.Request.Clone is the only Clone in this tree that
// takes a context argument, so the shape is "Clone with a single .Context()
// argument"; the ~18 slices/strings/proto/Header Clone calls in this tree do
// not match it.
var callShapes = []string{
	"http.NewRequestWithContext",
	"http.NewRequest",
	"websocket.Dial",
	"<receiver>.DialContext",
	"<receiver>.Dial",
	"net.DialTimeout",
	"net.Dial",
	"tls.Dial",
	"tls.DialWithDialer",
	"<request>.Clone(<x>.Context())",
	"<any call naming CloudEndpoint or cli.CloudEndpoint in an argument>",
}

// foundSite is one construction site the walk located.
type foundSite struct {
	file   string // relative to the walk root
	symbol string // callee text + "#" + occurrence ordinal
	line   int    // for error messages only; never part of the key
}

func (s foundSite) key() string { return s.file + ":" + s.symbol }

// calleeText renders a call's callee as source-like text: "http.NewRequest",
// "d.DialContext", "req.Clone".
func calleeText(fn ast.Expr) string {
	switch e := fn.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return x.Name + "." + e.Sel.Name
		}
		// A non-identifier receiver — (&net.Dialer{}).DialContext,
		// websocket.DefaultDialer.DialContext. Keep only the method so the
		// symbol stays stable under receiver rewording.
		return "<recv>." + e.Sel.Name
	}
	return ""
}

// methodName returns the method a selector call names, or "".
func methodName(fn ast.Expr) string {
	if sel, ok := fn.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

// isContextCall reports whether e is a call to something named Context —
// the discriminator that separates http.Request.Clone from every other Clone.
func isContextCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	return methodName(call.Fun) == "Context"
}

// namesCloudEndpoint reports whether e is the CloudEndpoint constant in either
// spelling.
func namesCloudEndpoint(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "CloudEndpoint"
	case *ast.SelectorExpr:
		if x, ok := v.X.(*ast.Ident); ok {
			return x.Name == "cli" && v.Sel.Name == "CloudEndpoint"
		}
	}
	return false
}

// isCensusSite decides whether a call is a construction site this census
// covers, over the shapes named in callShapes.
func isCensusSite(call *ast.CallExpr) bool {
	callee := calleeText(call.Fun)
	switch callee {
	case "http.NewRequestWithContext", "http.NewRequest",
		"websocket.Dial",
		"net.DialTimeout", "net.Dial",
		"tls.Dial", "tls.DialWithDialer":
		return true
	}
	// Any receiver's Dial / DialContext — the dialer-method arm.
	if m := methodName(call.Fun); m == "Dial" || m == "DialContext" {
		return true
	}
	// The request-clone arm: Clone taking exactly one .Context() argument.
	if methodName(call.Fun) == "Clone" && len(call.Args) == 1 && isContextCall(call.Args[0]) {
		return true
	}
	// The endpoint arm: any call naming CloudEndpoint in an argument position.
	return slices.ContainsFunc(call.Args, namesCloudEndpoint)
}

// walkCensus parses every non-test Go file under root and returns the
// construction sites it finds, keyed stably.
//
// It excludes _test.go files and testdata directories, and NOTHING ELSE — in
// particular it applies no package allowlist, which is the narrowing whose
// removal this gate exists to hold.
func walkCensus(root string) ([]foundSite, error) {
	var sites []foundSite
	// occurrence counts callee text per file so two identical calls in one file
	// get stable, distinct ordinals.
	occurrence := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", rel, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isCensusSite(call) {
				return true
			}
			callee := calleeText(call.Fun)
			ok2 := rel + "|" + callee
			occurrence[ok2]++
			sites = append(sites, foundSite{
				file:   rel,
				symbol: fmt.Sprintf("%s#%d", callee, occurrence[ok2]),
				line:   fset.Position(call.Pos()).Line,
			})
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].key() < sites[j].key() })
	return sites, nil
}

// checkReferential asserts the manifest's referential rule in BOTH directions.
//
// Returned problems are the gate's findings; an empty slice means the manifest
// is internally consistent.
func checkReferential(rows []censusRow) []string {
	byKey := make(map[string]censusRow, len(rows))
	for _, r := range rows {
		byKey[r.key()] = r
	}
	var problems []string
	for _, r := range rows {
		if r.Disposition != dispReaches {
			if r.Reaches != "" {
				problems = append(problems, fmt.Sprintf(
					"%s is dispositioned %q but carries a reaches referent %q; a stray referent on a non-referring row is as much a mislabel as a missing one",
					r.key(), r.Disposition, r.Reaches))
			}
			continue
		}
		if r.Reaches == "" {
			problems = append(problems, fmt.Sprintf(
				"%s is dispositioned %s but carries no reaches referent, so it claims coverage by an unnamed site",
				r.key(), dispReaches))
			continue
		}
		target, ok := byKey[r.Reaches]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s reaches %q, which is not a row in this manifest — a constructor cannot be covered by a site nobody dispositioned",
				r.key(), r.Reaches))
			continue
		}
		if target.Disposition == dispExcluded {
			problems = append(problems, fmt.Sprintf(
				"%s reaches %q, whose own disposition is %s — that records a constructor as covered while the request it produces is recorded as out of scope",
				r.key(), r.Reaches, dispExcluded))
		}
	}
	sort.Strings(problems)
	return problems
}
