// SPDX-License-Identifier: Apache-2.0

package embed

// identity.go answers ONE question: given the config this process will construct
// an embedder from, what will the vectors that embedder produces actually BE?
//
// THE ANSWER IS NOT THE CONFIG. Three of the four fields ride the config
// unchanged, but the MODEL does not: config carries no provider model name, and
// each arm fills its own default when the resolved Model is empty (the ordinary
// no-[embedder]-section case). An identity built from the resolved section alone
// would state an empty model while the arm embedded under voyage-code-3 — and
// because a graph RECORDS the first identity offered to it and is authoritative
// afterwards, that wrong answer is permanent short of an explicit migration.
//
// SO THE DEFAULT IS RESOLVED THROUGH THE SAME FUNCTION THE ARMS FILL FROM.
// defaultModelFor below is the one implementation of "what this arm embeds with
// when the operator named no model"; every factory calls it, and so does
// ResolveIdentity. A second copy of that rule here — a literal, a constant read
// off one arm — is exactly the parallel constant that would drift, and the drift
// would be silent in both directions: the identity states one model, the request
// body carries another, and every length check stays quiet.

// Identity is what a set of vectors ARE: the provider that produced them, the
// model, the width in bits and the representation.
//
// It is the client-side vocabulary for the same tuple the wire carries as
// knowledgev1.EmbedIdentity. It stays a plain struct in this package rather than
// the generated type because this package is where the answer is DERIVED, and it
// has no business importing the wire contract to describe its own configuration.
type Identity struct {
	Provider  Provider
	Model     string
	Dimension int
	Dtype     string
}

// ResolveIdentity reports the identity the embedder built from cfg will embed
// under.
//
// IT VALIDATES FIRST, for the same reason NewEmbedder does: a config this build
// cannot honor has no identity to state, and answering one anyway would let a
// refused width or an unknown provider be RECORDED on a graph as fact.
//
// THE GATE IS CONFIG.VALIDATE, NOT THE ARM'S FACTORY, and the difference is real
// rather than pedantic: an arm may refuse a config this admits, in either
// direction (the gemini and openai-compatible arms serve the unquantized
// representation only, so they refuse the admitted "ubinary"; the cohere arm
// decodes a quantized response only, so it refuses the admitted "float32"). So
// a resolved identity means "nothing about the SHAPE is refusable", not "an
// embedder was built". The caller that needs both asks for both — llmproviders
// builds the embedder and resolves the identity from the same config, and drops
// the axis when either half fails.
//
// THAT GAP MUST NEVER BE TOTAL FOR A REGISTERED ARM, which is a different claim
// and a separately enforced one: an arm that refuses EVERY admitted dtype can
// never be built at all, and the catalog would be advertising a provider this
// build cannot serve. Two arms were in exactly that state.
// TestEveryRegisteredProvider_ConstructsAtSomeAdmittedDtype is what fails when
// it recurs; neither set's own tests can see it, because neither set knows the
// other exists.
func ResolveIdentity(cfg *Config) (Identity, error) {
	if err := cfg.Validate(); err != nil {
		return Identity{}, err
	}
	model := cfg.Model
	if model == "" {
		model = defaultModelFor(cfg.Provider)
	}
	return Identity{
		Provider:  cfg.Provider,
		Model:     model,
		Dimension: cfg.Dimension,
		Dtype:     cfg.Dtype,
	}, nil
}

// defaultModelFor returns the model an arm embeds with when the resolved config
// names none. Every factory in this package fills its empty cfg.Model through
// this function, so the value here IS the value that reaches the provider.
//
// THE FAKE ANSWERS THE EMPTY STRING, and that is a real answer rather than a
// missing case: the deterministic fake derives its bytes from a hash of the text
// and calls no model at all, so naming one would be a claim about a request that
// is never made.
//
// AN UNREGISTERED PROVIDER ALSO ANSWERS THE EMPTY STRING and never reaches a
// caller: ResolveIdentity validates before asking, and Config.Validate refuses
// any provider outside the closed set above. A NEW arm that adds itself to that
// set without adding itself here is caught by the per-arm census test, not by a
// silent empty model on a first embed.
func defaultModelFor(p Provider) string {
	switch p {
	case ProviderVoyage:
		return DefaultModel
	case ProviderCohere:
		return cohereDefaultModel
	case ProviderGemini:
		return geminiDefaultModel
	case ProviderOpenAICompatible:
		return openAICompatDefaultModel
	case ProviderFake:
		return ""
	}
	return ""
}
