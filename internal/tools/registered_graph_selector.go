// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// registered_graph_selector.go validates the graph selector a CUSTOM-graph
// search carries, client-side, against the same two facts the server's
// resolveRegisteredCustom (cmd/knowledge-server/internal/tools/tools_graph_routing.go)
// resolves against: is the TYPE registered, and does the named INSTANCE exist.
// The server draws those as two distinct classifications — ErrGraphSelectorInvalid
// for an unregistered type (a typo), ErrGraphNotFound for a registered type whose
// named graph was never collected — and this file reproduces both, so the same
// selector is refused with the same meaning whichever side resolves it.
//
// It exists because the client search arms intercept BEFORE the call would ever
// reach that server gate: composeRegisteredGraphSearch serves a custom graph from
// client-shipped segments and never dispatches to the server, so without this the
// server's rejection is simply bypassed. What the client used to do instead —
// claim any non-builtin string, find no segments, and render a clean zero — made
// a typo, a never-collected graph and a genuine no-match byte-identical.

// validateRegisteredGraphSelector reports whether (gt, name) names a registered
// custom graph that has actually been collected. A nil return means the selector
// resolves; every non-nil return is a message a caller can act on, naming the
// offending value and either the accepted vocabulary or the collect path.
//
// It runs BEFORE the query embedding in composeRegisteredGraphSearch: an invalid
// selector must not bill an embed on a metered embedder.
func validateRegisteredGraphSelector(ctx context.Context, deps ClientDeps, gt kgtypes.GraphType, name string) error {
	registered, err := registeredGraphTypeNames(ctx, deps)
	if err != nil {
		return fmt.Errorf("graph %q: the graph-type registry could not be read, so the selector cannot be "+
			"verified: %w", string(gt), err)
	}
	if !slices.Contains(registered, string(gt)) {
		return fmt.Errorf("unsupported graph type %q: %s", string(gt), acceptedGraphVocabulary(registered))
	}
	collected, err := listGraphNamesOfType(ctx, deps, string(gt))
	if err != nil {
		return fmt.Errorf("%s graph %q: the collected %s graphs could not be enumerated, so the selector "+
			"cannot be verified: %w", string(gt), name, string(gt), err)
	}
	if !slices.Contains(collected, name) {
		return fmt.Errorf("%s graph %q not found: %s", string(gt), name, collectAdvice(string(gt), collected))
	}
	return nil
}

// registeredGraphTypeNames lists the names of every registered custom graph type
// via the graph-type registry.
//
// An UNREACHABLE registry is an error, never an empty list. The two states mean
// opposite things to the caller above — "this type is not registered" versus
// "whether it is registered is unknown" — and collapsing them would refuse every
// custom graph with a confidently wrong reason.
func registeredGraphTypeNames(ctx context.Context, deps ClientDeps) ([]string, error) {
	crud := deps.GraphTypeCRUD()
	if crud == nil {
		return nil, fmt.Errorf("graph-type registry unavailable")
	}
	defs, err := crud.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		if n := graphTypeDefName(d); n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// graphTypeDefName projects a registered type's name, nil-safe.
func graphTypeDefName(d *knowledgev1.GraphTypeDef) string {
	if d == nil {
		return ""
	}
	return d.GetName()
}

// acceptedGraphVocabulary renders the full accepted graph-selector vocabulary:
// every built-in (projected from kgtypes.BuiltinGraphTypeNames, so it cannot
// drift from the constants) plus the registered custom types passed in.
func acceptedGraphVocabulary(registered []string) string {
	var b strings.Builder
	b.WriteString(builtinGraphVocabularyClause())
	if len(registered) == 0 {
		b.WriteString("; no custom graph types are registered on this server — " +
			`custom_collector(operation:"register") adds one`)
		return b.String()
	}
	b.WriteString(" plus the registered custom types (")
	b.WriteString(strings.Join(registered, ", "))
	b.WriteString(`); custom_collector(operation:"list") enumerates the custom types`)
	return b.String()
}

// builtinGraphVocabularyClause is the built-in half of the accepted vocabulary,
// projected from kgtypes.BuiltinGraphTypeNames so it cannot drift from the
// constants. It is factored out because TWO refusals need it and their tails
// differ: acceptedGraphVocabulary can state what custom types exist, while a
// refusal issued when the registry was UNREADABLE must not — see
// unreadableRegistryVocabulary. One clause, two tails, no second spelling.
func builtinGraphVocabularyClause() string {
	return "valid graphs are the built-ins (" + strings.Join(kgtypes.BuiltinGraphTypeNames(), ", ") + ")"
}

// unreadableRegistryVocabulary renders the accepted vocabulary when the custom
// graph-type registry could NOT be read. It deliberately does NOT reuse
// acceptedGraphVocabulary's empty-registry tail — that one says "no custom graph
// types are registered on this server", a confident claim about something this
// call failed to measure. The built-ins are still authoritative; the custom half
// is reported as unverified, naming why.
func unreadableRegistryVocabulary(err error) string {
	return builtinGraphVocabularyClause() +
		"; the custom graph-type registry could not be read, so any registered custom types are " +
		"UNVERIFIED against this value: " + err.Error()
}

// collectAdvice renders the not-found tail for a registered type: which graphs of
// that type DO exist, and the collect call that would create the missing one.
func collectAdvice(gt string, collected []string) string {
	if len(collected) == 0 {
		return fmt.Sprintf("the graph type is registered but no %s graph has been collected yet — "+
			"run collect(type:%q, id:...) first", gt, gt)
	}
	return fmt.Sprintf("collected %s graphs are: %s — name one of those, or run collect(type:%q, id:...) "+
		"to add this one", gt, strings.Join(collected, ", "), gt)
}
