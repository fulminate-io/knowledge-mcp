// SPDX-License-Identifier: Apache-2.0

// engine_pin.go — the context pin: the caller's narrowing of the union to the
// parse context they meant.
//
// The pin selects by SET MEMBERSHIP, never by equality against a single stored
// context. A deduped variant records every context that compiled to its tree,
// so go `defer $X.Close()` — grammatical under both the declaration and the
// statement wrapper, compiling identically under each — carries [decl, stmt].
// Equality against its first entry would turn context:"stmt" into a zero result
// for a pattern the statement context expresses perfectly.
//
// The filter runs after the whole union is enumerated, which is what lets a pin
// that selects nothing say which pin WOULD have worked. That sentence is the
// pin's real product: a wrong-context zero is otherwise indistinguishable from
// a pattern with no sites.

package ast

import (
	"fmt"
	"slices"
	"strings"
)

// applyContextPin narrows an already-deduped candidate set to the variants
// carrying pin among their Contexts, closing the trees it drops. An empty pin
// is the union and passes through untouched.
//
// When the pin selects nothing the error carries three things the caller can
// act on: every wrapper tried, why each contributed nothing (an excluded
// candidate says it was excluded rather than reading as a parse failure), and
// the union of the contexts that DID produce a candidate.
func applyContextPin(variants []patternVariant, pin string, names, rejects []string) ([]patternVariant, error) {
	if pin == "" {
		return variants, nil
	}

	var (
		kept     []patternVariant
		produced []string
		seen     = map[string]struct{}{}
	)
	for i := range variants {
		if slices.Contains(variants[i].Contexts, pin) {
			kept = append(kept, variants[i])
			continue
		}
		for j, c := range variants[i].Contexts {
			rejects = append(rejects, fmt.Sprintf("%s[%s]: excluded by context pin %q",
				variants[i].Wrappers[j], c, pin))
			if _, dup := seen[c]; !dup {
				seen[c] = struct{}{}
				produced = append(produced, c)
			}
		}
		variants[i].Close()
	}
	if len(kept) > 0 {
		return kept, nil
	}
	return nil, fmt.Errorf("%w (tried %s; %s; this pattern compiles in %s — pin context to one of those, or omit context to match their union)",
		errCompileNoWrapper, strings.Join(names, ","), strings.Join(rejects, "; "), strings.Join(produced, ","))
}
