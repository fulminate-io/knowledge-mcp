// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestEveryRegisteredProvider_ConstructsAtSomeAdmittedDtype is the INTERSECTION
// control between two sets that are declared in different packages and had
// silently stopped overlapping: the dtypes this build ADMITS
// (config.AcceptedEmbedDtypes, enforced by Config.Validate before any factory
// runs) and the dtypes each registered arm ACCEPTS (its own capability gate,
// enforced inside the factory).
//
// WHAT IT WOULD CATCH, and did. The gemini and openai-compatible arms gated on
// the literal "float" while the admitted set was {"ubinary", "float32"}. Every
// admitted dtype was refused by those arms and the arms' only accepted dtype was
// refused before they ran, so NewEmbedder could not construct either one under
// any configuration an operator could write. The catalog advertised two
// providers the build could not serve, and nothing was red: each arm's own test
// asserted the refusal, and the admission gate's own test asserted the admitted
// set, because neither set knows the other exists. Only a test that intersects
// them can fail.
//
// IT IS PARAMETERIZED OVER THE REGISTRY'S OWN LIST, not a hand-kept copy, for
// the reason the same file's sibling census gives: a new arm that registers
// itself and gates on a spelling nobody else uses is exactly the failure this
// exists to catch, and a hand-kept copy would simply not mention it.
//
// EVERY REFUSAL IS ALSO INSPECTED, so an arm cannot pass by narrowing silently:
// a refused admitted dtype must be an ErrInvalidConfig that NAMES the refused
// value and names a dtype the arm does accept. The accepted spelling is taken
// from THIS SUBTEST'S OWN observed constructions rather than from a literal, so
// the assertion cannot drift away from what the arm actually does.
func TestEveryRegisteredProvider_ConstructsAtSomeAdmittedDtype(t *testing.T) {
	ctx := context.Background()

	providers := ListProviders()
	require.NotEmpty(t, providers,
		"no arm registered, so this census intersected nothing")
	require.NotEmpty(t, config.AcceptedEmbedDtypes,
		"the build admits no dtype at all, so every subtest below would fail for a reason that is not the one under test")

	for _, p := range providers {
		t.Run(string(p), func(t *testing.T) {
			var accepted []string
			refusals := map[string]error{}

			for _, dtype := range config.AcceptedEmbedDtypes {
				_, err := NewEmbedder(ctx, &Config{
					Provider:  p,
					APIKey:    "test-key", // satisfies the credential rule for the API arms
					Dimension: config.AcceptedEmbedDimension,
					Dtype:     dtype,
				})
				if err != nil {
					refusals[dtype] = err
					continue
				}
				accepted = append(accepted, dtype)
			}

			require.NotEmpty(t, accepted,
				"provider %q is registered but constructs at NONE of the dtypes this build admits (%s); "+
					"an operator naming it in [embedder] gets a hard error on every path. Refusals: %s",
				p, strings.Join(config.AcceptedEmbedDtypes, ", "), renderRefusals(refusals))

			// A refusal is a BAD-INPUT error, never a silent narrowing: it names
			// the value it refused and points at one the arm can serve.
			for dtype, err := range refusals {
				require.ErrorIs(t, err, ErrInvalidConfig,
					"a dtype this arm cannot serve must be refused as a config error, got %v", err)
				assert.Contains(t, err.Error(), dtype,
					"the refusal must name the dtype it refused")
				assert.True(t, namesOneOf(err.Error(), accepted),
					"the refusal of %q must name a dtype this arm DOES accept (%v) so the operator "+
						"is not left guessing the spelling; got: %v", dtype, accepted, err)
			}
		})
	}
}

// namesOneOf reports whether msg mentions at least one of want.
func namesOneOf(msg string, want []string) bool {
	for _, w := range want {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// renderRefusals formats the per-dtype refusals for the failure message, so a
// red run states which gate refused which value rather than only that nothing
// constructed.
func renderRefusals(refusals map[string]error) string {
	if len(refusals) == 0 {
		return "(none recorded)"
	}
	parts := make([]string, 0, len(refusals))
	for _, dtype := range config.AcceptedEmbedDtypes {
		if err, ok := refusals[dtype]; ok {
			parts = append(parts, fmt.Sprintf("%s -> %v", dtype, err))
		}
	}
	return strings.Join(parts, "; ")
}
