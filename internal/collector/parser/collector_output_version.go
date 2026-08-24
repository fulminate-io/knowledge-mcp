package parser

// CollectorOutputVersion names the IDENTITY OF WHAT THE COLLECTOR EMITS — the
// rows this client produces for a given tree — and not how any hash over them
// is computed. Those are different facts with different owners:
// ContributionHashSchemeVersion is a two-party agreement about the hashing
// scheme, compared client-against-server; this is a one-party fact, compared by
// this client against what this same client last recorded for a graph.
//
// BUMP IT WHENEVER TestCollectorOutputIdentity_Digest GOES RED, in the SAME
// change as the new pinnedCollectorOutputDigest. The gate is what makes the
// bump an obligation rather than a memory: the emitted values a per-file
// contribution hash structurally cannot see — node Id, Summary, Keywords and
// metadata (docs/collect-contribution-hash.md sections A and C) — move without
// moving any per-file hash, so nothing else in the system notices them.
//
// CLIENT-ONLY, WITH NO SERVER COUNTERPART, because the server holds no chunker
// and therefore cannot know that this client's collector changed. A collect
// whose recorded value for a graph@branch differs from this const takes one
// decline-suppressed full re-land and then converges.
//
// It starts at 1 because zero is reserved to mean "the producer did not stamp
// it", which the sink refuses loudly rather than reading as unchanged.
const CollectorOutputVersion uint32 = 1
