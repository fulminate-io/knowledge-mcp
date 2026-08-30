// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// resolvedEmbedDtype returns the representation this client's vectors are
// produced in, read from the resolved [embedder] section.
//
// IT EXISTS SO THE THREE HNSW DOCUMENT PRODUCERS AGREE. The embed-writeback ship
// path, the segment rebuild driver and the segment repair all hand documents to
// the same vector format, and that format derives a segment's dtype from the
// documents it is given. Three independent answers to "what representation is
// this?" would let a rebuilt or repaired segment be tagged differently from the
// freshly-embedded one it is supposed to be indistinguishable from — and the
// symptom would not be an error, it would be one segment ranked by Hamming
// distance while its twin is ranked by dot product.
//
// A MALFORMED SECTION REPORTS A FAILURE RATHER THAN ANSWERING "", and the
// difference is not cosmetic. The format reads an empty dtype as ubinary, so
// returning "" on a resolve error would convert a config FAULT into the
// ASSERTION that these vectors are ubinary. On the rebuild and repair paths that
// assertion is applied to vectors ALREADY STORED — bytes this run did not
// produce and cannot re-derive — so a float32 corpus would be re-sealed as
// ubinary and its IEEE bit patterns ranked by Hamming distance: every byte
// preserved, every length check quiet, the ordering wrong, nothing reporting a
// problem. A caller that cannot determine the representation of the bytes it is
// about to re-seal has no correct answer available and must say so.
//
// AN ABSENT CONFIG IS NOT THAT CASE, and conflating the two would be its own
// defect. ResolveEmbedder is nil-safe and DEFINES what an absent [embedder]
// section means: the documented defaults, whose dtype is ubinary. That is a
// resolution rule, not a guess — and it cannot be wrong here, because float32 is
// reachable only by explicitly configuring it, so a client with no config never
// produced a float32 vector to mis-tag. Erroring on an absent config would
// instead break every deployment that never wrote an [embedder] section, on
// paths (rebuild, repair) that must keep working without one.
//
// Active() PANICS when nothing is loaded, which is why the guard is a nil
// *Config handed to the nil-safe method rather than a call through Active().
func resolvedEmbedDtype() (string, error) {
	var cfg *config.Config
	if config.Loaded() {
		cfg = config.Active()
	}
	sec, err := cfg.ResolveEmbedder()
	if err != nil {
		return "", fmt.Errorf("the [embedder] section does not resolve: %w", err)
	}
	return sec.Dtype, nil
}
