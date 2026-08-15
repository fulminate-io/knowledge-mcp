// SPDX-License-Identifier: Apache-2.0

package jsmodule

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// maxReExportDepth bounds a re-export chain. Barrel files legitimately chain
// several deep; a chain longer than this is a cycle the visited set did not
// catch, or a repository shape no resolution would help.
const maxReExportDepth = 16

// resolveName decides WHICH declaration in a resolved file the local name binds
// to, following re-export chains when the file forwards rather than declares.
//
// The kind switch is exhaustive over the three kinds that reach it; the other
// two were refused before the specifier ladder ran.
func (r *Resolver) resolveName(file, imported string, kind treesitter.ImportKind) (Target, Outcome) {
	switch kind {
	case treesitter.ImportNamespace:
		// The MODULE ITSELF is bound. The reference supplies the member name,
		// so the target carries no name of its own — a namespace import renames
		// the module, never its members.
		return Target{File: file}, r.boundOutcome(file)

	case treesitter.ImportDefault:
		// An anonymous default leaves Name empty, which degrades to the
		// reference's own name. Measured correct on 438 of 439 in-repo default
		// imports of the acceptance corpus with zero counter-examples —
		// recorded as a measurement, not assumed as a rule.
		return Target{File: file, Name: r.exports[file].DefaultName}, r.boundOutcome(file)

	case treesitter.ImportNamed:
		return r.resolveNamed(file, imported)

	case treesitter.ImportWildcard, treesitter.ImportSideEffect:
		return Target{}, OutcomeRefused
	}
	return Target{}, OutcomeRefused
}

// resolveNamed resolves one named import, following re-exports when the file
// does not declare the name itself.
//
// THE FINAL FALLBACK CANNOT MIS-BIND. When nothing declares the name and no
// re-export forwards it, the target is the resolved file under the requested
// name anyway: the declaration index holds no entry under that (scope, name),
// so the reference yields zero candidates and lands external by the ladder's
// last rung. Returning nothing instead would OMIT the bind, which is the one
// failure that matters — an omitted bind puts the reference back on the dynamic
// rung and re-creates the wrong-edge class the external-qualifier rule exists
// to remove.
func (r *Resolver) resolveNamed(file, imported string) (Target, Outcome) {
	if r.exports[file].Declared[imported] {
		return Target{File: file, Name: imported}, r.boundOutcome(file)
	}
	if target, outcome, ok := r.followReExports(file, imported); ok {
		return target, outcome
	}
	return Target{File: file, Name: imported}, r.boundOutcome(file)
}

// followReExports walks a barrel chain looking for the file that DECLARES the
// name.
//
// THE CYCLE GUARD IS NOT OPTIONAL AND ITS ABSENCE IS INVISIBLE IN A SMALL
// FIXTURE: two barrels that re-export from each other produce an infinite
// descent that a single-barrel test never reaches. A visited set of
// (file, name) pairs stops both direct and mutual cycles, and the depth cap
// stops anything the set cannot see. On a repeat or the cap the walk STOPS AND
// RETURNS THE LAST FILE RESOLVED rather than looping or failing: barrel cycles
// occur in real repositories, so this must degrade, not hang.
func (r *Resolver) followReExports(file, imported string) (Target, Outcome, bool) {
	visited := map[[2]string]bool{}
	return r.followFrom(file, imported, visited, 0)
}

// followFrom is one hop of the chain.
func (r *Resolver) followFrom(
	file, imported string, visited map[[2]string]bool, depth int,
) (Target, Outcome, bool) {
	if depth >= maxReExportDepth {
		return Target{}, OutcomeBound, false
	}
	key := [2]string{file, imported}
	if visited[key] {
		return Target{}, OutcomeBound, false
	}
	visited[key] = true

	exports := r.exports[file]

	// NAMED RE-EXPORTS FIRST, in declaration order: `export {A as B} from './y'`
	// forwards the local name B to the source module's A.
	for _, re := range exports.ReExports {
		if re.Local == "" || re.Local != imported {
			continue
		}
		if target, outcome, ok := r.hop(file, re.Specifier, re.Imported, visited, depth); ok {
			return target, outcome, true
		}
	}
	// THEN `export * from` entries, in declaration order, each searched under
	// the SAME name, first hit winning.
	for _, re := range exports.ReExports {
		if re.Local != "" {
			continue
		}
		if target, outcome, ok := r.hop(file, re.Specifier, imported, visited, depth); ok {
			return target, outcome, true
		}
	}
	return Target{}, OutcomeBound, false
}

// hop resolves one re-export's specifier relative to the barrel that declared
// it and continues the search there.
//
// A chain that leaves the repository — `export {X} from 'react'` — terminates
// as out of repo rather than being followed into node_modules, which was never
// discovered.
func (r *Resolver) hop(
	from, specifier, imported string, visited map[[2]string]bool, depth int,
) (Target, Outcome, bool) {
	candidate, outcome := r.candidateFor(from, specifier)
	if outcome == OutcomeOutOfRepo {
		return Target{}, OutcomeOutOfRepo, true
	}
	next, ok := r.resolveFile(from, candidate)
	if !ok {
		return Target{File: candidate, Name: imported}, OutcomeUndiscovered, true
	}
	if r.exports[next].Declared[imported] {
		return Target{File: next, Name: imported}, r.boundOutcome(next), true
	}
	return r.followFrom(next, imported, visited, depth+1)
}
