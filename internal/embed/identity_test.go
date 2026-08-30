// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// armModel returns the model a CONSTRUCTED arm will actually send, read off the
// concrete embedder's own field — the same field its request builder reads.
//
// THE DEFAULT CASE IS A FAILURE, not a skip. A new arm registered without being
// added here (and to defaultModelFor) would otherwise resolve an empty model,
// and an empty model recorded as a graph's identity at first embed is permanent
// short of an explicit migration. Being unable to answer is the finding.
func armModel(t *testing.T, e BinaryEmbedder) string {
	t.Helper()
	switch a := e.(type) {
	case *voyageEmbedder:
		return a.Model
	case *cohereEmbedder:
		return a.Model
	case *geminiEmbedder:
		return a.Model
	case *openAICompatEmbedder:
		return a.Model
	case *fakeEmbedder:
		// The fake derives bytes from a hash and calls no model, so the empty
		// string is its true answer rather than a missing one.
		return ""
	default:
		t.Fatalf("arm %T is registered but this census cannot read the model it sends; "+
			"add it here and to defaultModelFor", e)
		return ""
	}
}

// TestResolveIdentity_StatesTheModelTheArmWillActuallySend is the anti-drift
// control for the whole identity seam, run over EVERY registered provider.
//
// WHAT IT WOULD CATCH. The identity a client states is what a graph RECORDS at
// its first embed and is authoritative afterwards, while the model that produced
// the bytes is whatever the arm filled in. If those two are derived separately,
// they can disagree — and the disagreement is silent in both directions: the
// vectors are the right LENGTH, the record reads plausible, and every later
// query embeds against a model the corpus was never embedded with.
//
// It drives NewEmbedder with an EMPTY model deliberately: that is the only case
// where the two derivations can differ at all, because a model the operator
// named rides both paths verbatim.
//
// IT SWEEPS EVERY ADMITTED DTYPE RATHER THAN ONLY THE DEFAULT, because an arm
// is constructible at the dtypes IT serves, not at the one an absent [embedder]
// section resolves to. The gemini and openai-compatible arms serve the
// unquantized representation only, so a census pinned to the ubinary default
// would take the refusal branch for both and leave the two arms whose identity
// is least exercised permanently unmeasured — while its own comment claimed
// they would be picked up "automatically" the day they became constructible.
// Sweeping the admitted set is what makes that claim true instead of aspirational.
//
// AN ARM THAT REFUSES A GIVEN DTYPE IS STILL CHECKED, NOT SKIPPED: the refusal
// must be a config refusal rather than some other failure. The `constructed`
// counter is the known-positive that stops a build where EVERY arm refuses
// EVERY dtype from passing this test having compared nothing.
func TestResolveIdentity_StatesTheModelTheArmWillActuallySend(t *testing.T) {
	constructed := 0
	require.NotEmpty(t, config.AcceptedEmbedDtypes,
		"the build admits no dtype, so the sweep below would compare nothing for a reason that is not the one under test")

	for _, p := range ListProviders() {
		t.Run(string(p), func(t *testing.T) {
			built := 0
			for _, dtype := range config.AcceptedEmbedDtypes {
				cfg := &Config{
					Provider:  p,
					APIKey:    "test-key", // satisfies the credential rule for the API arms
					Dimension: config.AcceptedEmbedDimension,
					Dtype:     dtype,
				}
				e, err := NewEmbedder(context.Background(), cfg)
				if err != nil {
					require.ErrorIs(t, err, ErrInvalidConfig,
						"an arm that cannot be built at an admitted shape must say so as a config refusal")
					continue
				}
				constructed++
				built++

				id, rerr := ResolveIdentity(cfg)
				require.NoError(t, rerr)

				assert.Equal(t, armModel(t, e), id.Model,
					"the identity must state the model this arm sends, or a graph records one embedder and is fed another's vectors")
				assert.Equal(t, p, id.Provider)
				assert.Equal(t, config.AcceptedEmbedDimension, id.Dimension)
				assert.Equal(t, dtype, id.Dtype,
					"the identity must state the representation the config asked for")
			}
			assert.NotZero(t, built,
				"provider %q was measured at no dtype at all — see TestEveryRegisteredProvider_ConstructsAtSomeAdmittedDtype", p)
		})
	}
	require.NotZero(t, constructed,
		"no registered arm could be constructed at any admitted shape, so this census compared nothing")
}

// TestResolveIdentity_HonorsAnExplicitModel is the known-positive for the
// default-filling above: with a model named, the identity is that model and not
// the arm's default. Without it, every assertion in this file would still pass
// against a ResolveIdentity that ignored cfg.Model and always answered the
// default.
func TestResolveIdentity_HonorsAnExplicitModel(t *testing.T) {
	cfg := &Config{
		Provider:  ProviderVoyage,
		Model:     "voyage-3-large",
		APIKey:    "test-key",
		Dimension: config.AcceptedEmbedDimension,
		Dtype:     config.AcceptedEmbedDtype,
	}
	id, err := ResolveIdentity(cfg)
	require.NoError(t, err)
	assert.Equal(t, "voyage-3-large", id.Model)
	assert.NotEqual(t, DefaultModel, id.Model,
		"the fixture must name a model that is NOT the arm's default, or it proves nothing")
}

// TestResolveIdentity_RefusesAConfigThatCannotBeHonored pins that an identity is
// never produced for a config this build would refuse to embed with. A stated
// identity is RECORDED on a graph, so answering one for a refused width or an
// unknown provider would durably record a shape nothing can produce.
func TestResolveIdentity_RefusesAConfigThatCannotBeHonored(t *testing.T) {
	_, err := ResolveIdentity(&Config{
		Provider:  ProviderVoyage,
		APIKey:    "test-key",
		Dimension: 7, // outside AcceptedEmbedDimensions
		Dtype:     config.AcceptedEmbedDtype,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}
