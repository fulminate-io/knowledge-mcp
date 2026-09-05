// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// renderClientVersionState renders the `knowledge version` block describing the
// gateway's last version verdict on this client and this client's own last
// possession proof.
//
// WHY THIS SURFACE NEEDS ITS OWN RENDERER. `knowledge version` and
// manage(status) are two different functions in two different packages that
// deliberately share only the skew line, so a block added to one does not
// appear on the other. This is the command a user runs after being told their
// client is too old, which makes it the surface where a refusal is most likely
// to be looked for and least affordable to omit.
//
// The two records are kept apart for the same reason the manage surface keeps
// them apart: a refusal beside a successful proof means the version really is
// too old, while a refusal beside a FAILED proof means the proof is broken and
// upgrading will not help.
//
// When neither is set it returns the empty string, so a healthy client's
// `knowledge version` output is byte-identical to what it was before.
func renderClientVersionState() string {
	refusal, refused := clientver.CurrentRefusal()
	proof, proved := clientver.LastProof()
	if !refused && !proved {
		return ""
	}

	var b strings.Builder
	if refused {
		fmt.Fprintf(&b, "refused by the Fulminate gateway (%s): minimum %s, this client %s on %s — run `%s` to upgrade (at %s)\n",
			refusal.Reason,
			unknownIfBlank(refusal.Minimum),
			unknownIfBlank(refusal.ClientVersion),
			unknownIfBlank(refusal.Platform),
			unknownIfBlank(refusal.UpgradeCommand),
			refusal.At.Format(time.RFC3339))
		// The diagnostic is set only when THIS CLIENT could not read the refusal,
		// and that is exactly when the line above is at its least useful: the
		// minimum and the remedy both render as "(unknown)". Without this the user
		// is told they are refused, told nothing they can act on, and given no way
		// to tell a broken client from a genuinely old one.
		if refusal.Diagnostic != "" {
			fmt.Fprintf(&b, "  this client could not read the refusal: %s\n", refusal.Diagnostic)
		}
	}
	if proved {
		if proof.OK {
			fmt.Fprintf(&b, "version proof: verified at %s (%s on %s)\n",
				proof.At.Format(time.RFC3339), unknownIfBlank(proof.Version), unknownIfBlank(proof.Platform))
		} else {
			fmt.Fprintf(&b, "version proof: FAILED at %s: %s\n",
				proof.At.Format(time.RFC3339), unknownIfBlank(proof.Err))
		}
	}
	return b.String()
}

// unknownIfBlank states an absent field as an absence rather than rendering a
// blank that reads like an answer.
func unknownIfBlank(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
