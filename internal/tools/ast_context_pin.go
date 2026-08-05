// SPDX-License-Identifier: Apache-2.0

// ast_context_pin.go — validation for the ast tool's `context` pin.
//
// Two of the pin's three failure modes are answerable before any parse runs,
// and both are answered here so the caller gets the specific remedy rather than
// a generic zero: a value outside the four-context vocabulary, and a value the
// target language registers no wrapper for. The third — a valid, registered
// context that no wrapper HOSTS this particular pattern under — is only
// knowable after the union compiles, so the engine owns it and names the
// contexts that did produce a candidate.
//
// Both messages read from live sources (the engine's own context constants and
// the language registry) rather than a table here, so a language that gains or
// loses a wrapper cannot leave this file offering a context nothing compiles
// under.

package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// validateContextPin checks a caller-supplied context pin against the context
// vocabulary and against what lang actually registers. An empty pin is the
// default (match the union of every hosting context) and always passes.
func validateContextPin(pin string, lang treesitter.Language) error {
	if pin == "" {
		return nil
	}
	valid := ast.ValidContexts()
	if !slices.Contains(valid, pin) {
		return fmt.Errorf("context %q is not a parse context: pass one of %s, or omit context to match every context the pattern is expressible in",
			pin, strings.Join(valid, ", "))
	}
	registered, ok := ast.RegisteredContexts(lang)
	if !ok {
		// No config for this language: the handler's own grammar check owns
		// that failure and reports it in the language's own terms.
		return nil
	}
	if !slices.Contains(registered, pin) {
		return fmt.Errorf("language %s registers no %q context: it registers %s",
			lang, pin, strings.Join(registered, ", "))
	}
	return nil
}
