// SPDX-License-Identifier: Apache-2.0

// Per-axis facts the client pipeline config carries, resolved from config —
// plus the embedder those facts describe, since building it and describing it
// is one decision (buildEmbedAxis).
//
// They live beside pipeline.go rather than in it because that file is at the
// repo's hard 500-line cap. The grouping is not arbitrary: every function here
// answers the same shape of question — given the loaded config, what does this
// axis resolve to — and each is read exactly once, at pipeline construction.
//
// THEY DIFFER IN WHAT AN UNRESOLVABLE CONFIG MEANS, which is the one thing a
// reader must not flatten. The provider labels are advisory (they gate a
// shared-cause cross-trip) and degrade to "" — unknown, never participates. The
// DTYPE is not advisory: it decides which metric ranks a corpus, so a config
// fault is reported rather than answered. The IDENTITY is the strictest of the
// three: an unresolvable one takes the embed axis down with it, because a vector
// written without one can never be accepted by a graph that has no record.

package bootstrap

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// buildEmbedAxis constructs this pipeline's embedder and resolves the three
// facts the pipeline Config carries about it: the provider label, the
// representation its vectors are produced in, and the identity every vector it
// writes back will STATE.
//
// THE INDEX PIPELINE EMBEDS CORPUS TEXT, so it takes the document role.
//
// A MISCONFIGURED EMBEDDER DEGRADES RATHER THAN DIES, exactly as the summarizer
// build does: the client keeps serving non-LLM tools rather than taking down the
// MCP loop. The error is logged so a malformed [embedder] section is visible,
// not swallowed.
//
// THE EMBEDDER AND THE IDENTITY ARE ONE DECISION, because either alone is wrong.
// An embedder with no identity writes vectors a graph with no recorded identity
// can never accept — the server records a first-embed identity only when the
// batch OFFERS one — so the axis would pay a provider for bytes the server
// refuses, on every retry, forever. An identity with no embedder states what
// nothing produced, and would be offered on embed-axis scans that were never
// going to embed anything.
//
// SO AN EMBEDDER THAT CANNOT SAY WHAT IT PRODUCES DOES NOT RUN. Not embedding is
// recoverable — the nodes stay eligible and drain once the config is fixed;
// billing for refused writes is not. That case is also unreachable in practice,
// since the embedder is constructed from the same config the identity resolves
// from, so reaching it means the two disagree — precisely when nothing should be
// written.
func buildEmbedAxis(ctx context.Context) (
	emb embed.BinaryEmbedder, provider, dtype string, identity *knowledgev1.EmbedIdentity,
) {
	emb, err := llmproviders.BuildEmbedder(ctx, embed.InputRoleDocument)
	if err != nil {
		slog.Warn("client pipeline: embedder build failed; continuing without one", "error", err)
		emb = nil
	}
	identity, idErr := llmproviders.ResolvedEmbedIdentity(embed.InputRoleDocument)
	switch {
	case emb == nil:
		// No embedder, no vectors, no claim. A config can resolve a SHAPE whose arm
		// then refuses to build, so this is a real case rather than a tidy-up.
		identity = nil
	case idErr != nil || identity == nil:
		slog.Warn("client pipeline: the embed identity could not be resolved from the config the embedder was built from; embed axis disabled (vectors written without an identity can never be accepted by a graph that has none)",
			"error", idErr)
		emb, identity = nil, nil
	}
	provider, dtype = resolveEmbedLabels(emb != nil)
	return emb, provider, dtype, identity
}

// resolveSummaryProvider returns the summary axis's LLM provider identity for the
// shared-cause escalation gate, resolved from the SAME config consumer
// BuildSummarizer uses (config.ConsumerSummarizer). Degrade-not-die: an unloaded
// config or a resolve error yields "" (unknown) so that axis never participates
// in a cross-trip — never an error that blocks pipeline wiring.
func resolveSummaryProvider() string {
	if !config.Loaded() {
		return ""
	}
	sec, err := config.Active().Resolve(config.ConsumerSummarizer)
	if err != nil {
		return ""
	}
	return sec.Provider.String()
}

// resolveEmbedProvider returns the embed axis's provider identity for the
// shared-cause escalation gate, resolved from the SAME [embedder] section
// BuildEmbedder uses. It replaces a hardcoded "voyage" literal, which stopped
// being true the moment the provider became configurable — the escalation
// fires only when the summary and embed provider strings MATCH, so a stale
// literal would cross-trip two axes that are no longer the same provider.
// Degrade-not-die, mirroring resolveSummaryProvider: an unloaded config or a
// resolve error yields "" (unknown) so that axis never participates in a
// cross-trip.
func resolveEmbedProvider() string {
	if !config.Loaded() {
		return ""
	}
	sec, err := config.Active().ResolveEmbedder()
	if err != nil {
		return ""
	}
	return sec.Provider.String()
}

// resolveEmbedLabels returns the two [embedder]-derived scalars the pipeline
// config carries: the axis's provider identity and the representation its
// vectors are produced in.
//
// THEY ARE RESOLVED TOGETHER because they are gated by the same condition — an
// embedder actually being wired — and because they come from the same resolved
// section. Two separate guarded blocks could drift into disagreeing about
// whether an embedder exists, which would label the pipeline with one arm's
// provider and another's representation.
//
// BOTH ARE EMPTY WHEN NO EMBEDDER IS WIRED. An empty provider means that axis
// never participates in a shared-cause cross-trip (see resolveEmbedProvider),
// and an empty dtype is read as ubinary by the vector format under the on-disk
// tag-0 convention — and with no embedder there are no vectors to tag anyway.
//
// A DTYPE FAULT IS LOGGED AND THE DTYPE LEFT EMPTY, which is safe HERE and only
// here: this pipeline is being wired WITH an embedder built from that same
// section, so a section that does not resolve means the embedder build already
// failed and reported it, and no vector will be produced for the empty dtype to
// mis-tag. The rebuild and repair paths make the opposite choice and REFUSE,
// because they re-seal vectors that already exist independently of any embedder.
func resolveEmbedLabels(embedderWired bool) (provider, dtype string) {
	if !embedderWired {
		return "", ""
	}
	dtype, err := resolveEmbedDtype()
	if err != nil {
		slog.Warn("client pipeline: [embedder] representation unresolved; shipped segments carry no dtype tag",
			"error", err)
		dtype = ""
	}
	return resolveEmbedProvider(), dtype
}

// resolveEmbedDtype returns the embed axis's RESOLVED representation, read from
// the SAME [embedder] section BuildEmbedder builds the arm from — so the dtype
// tagged onto every shipped HNSW document is the one the arm actually produced
// its bytes in, and the two cannot drift.
//
// IT RESOLVES RATHER THAN ASSUMES, which is the whole point. The vector format
// derives a segment's dtype from the documents it is handed, so a literal here
// would seal a float32 corpus as ubinary and rank IEEE bit patterns by Hamming
// distance — no error, no panic, just wrong neighbors.
//
// A MALFORMED SECTION IS REPORTED, NOT ANSWERED AS "". The format reads an empty
// dtype as ubinary, so returning "" on a resolve fault would turn a config error
// into the assertion that this pipeline's vectors are ubinary — and that
// assertion decides which metric ranks the corpus.
//
// AN ABSENT CONFIG IS A DIFFERENT CASE and resolves normally: ResolveEmbedder is
// nil-safe and defines an absent [embedder] section as the documented defaults,
// whose dtype is ubinary. float32 is reachable only by configuring it
// explicitly, so a pipeline with no config never produced a float32 vector.
//
// Active() panics when nothing is loaded, which is why this hands a nil *Config
// to the nil-safe method rather than calling through Active().
func resolveEmbedDtype() (string, error) {
	var cfg *config.Config
	if config.Loaded() {
		cfg = config.Active()
	}
	sec, err := cfg.ResolveEmbedder()
	if err != nil {
		return "", err
	}
	return sec.Dtype, nil
}
