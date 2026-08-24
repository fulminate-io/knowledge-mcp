// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// resolveTypeTextThroughIndex resolves a type written AS TEXT, consulting the
// declaration index and — only where the index-blind answer names nothing the
// index declares — the referencing file's IMPORT BINDS.
//
// IT IS resolveImportBound's RULE, APPLIED TO TYPE TEXT. That rung consults
// ref.Binds for a bare name, overrides the looked-up name with the bind's own
// when the arm recorded one, and REQUIRES the bind's target scope to actually
// declare the result — falling through to today's answer otherwise. Holding to
// the same discipline is what makes the bind incapable of producing a wrong
// target: it fires only where today's answer names nothing declared, and only
// to a scope that does declare it. A file whose arm records a deliberately
// terminating scope therefore keeps its current answer, unchanged.
//
// resolveTypeText IS DELIBERATELY NOT CHANGED, and that is a design decision
// rather than a smaller edit. That helper is shared by a declaration's result
// types, its field types, its embeds and the leaves of its signature key — all
// of which run at INDEX-BUILD time, when the index is incomplete, and all of
// which feed the Go method-set derivation. Teaching its unqualified branch to
// read binds would move that derivation, which this work must not do; and being
// index-blind it could not require the bind's target to declare the name, so a
// language whose binds terminate on purpose would have its working same-file
// answer replaced by a scope that declares nothing.
//
// THE ORDER OF THE RULE IS THE RULE. It can never change an answer that today
// names a declared type, because the own-scope lookup is tried FIRST and
// returns on success — an imported name that shadows a local declaration
// resolves to the local one, exactly as it does now.
func resolveTypeTextThroughIndex(ix *declIndex, ref *treesitter.RefSite, text string) typeRef {
	tr := resolveTypeText(ref, text)
	if ix == nil || ref == nil {
		return tr
	}
	// (1) TODAY'S ANSWER WINS WHERE IT NAMES SOMETHING. The anti-shadowing
	// case: a bind of the same name may exist, and it does not get to override
	// a declaration the reference's own scope really holds.
	if tr.Scope != "" && len(ix.lookup(declKey{Scope: tr.Scope, Name: tr.Name})) > 0 {
		return tr
	}
	// (2) A QUALIFIED SPELLING IS ALREADY A BIND QUESTION, and resolveTypeText
	// answered it: the qualifier either bound to a scope or declined. There is
	// no second bind to consult for the same text.
	qualifier, rawName := splitQualifier(ref.Lang, text)
	if qualifier != "" {
		return tr
	}
	name := baseDeclName(rawName)
	if name == "" {
		return tr
	}
	bind, ok := ref.Binds[name]
	if !ok {
		return tr
	}
	// (3) THE BIND'S OWN NAME WINS WHEN THE ARM RECORDED ONE — an aliased
	// import binds the local spelling to a differently-named declaration.
	lookupName := name
	if bind.Name != "" {
		lookupName = bind.Name
	}
	// (4) AND THE TARGET MUST ACTUALLY DECLARE IT. Without this the rung would
	// answer with a scope that holds nothing, which is strictly worse than
	// today's decline: it is a wrong target rather than a missing one.
	if len(ix.lookup(declKey{Scope: bind.Scope, Name: lookupName})) > 0 {
		return typeRef{Scope: bind.Scope, Name: lookupName}
	}
	return tr
}
