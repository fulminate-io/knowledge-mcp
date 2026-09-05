// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// renderClientVersionStateLines renders the manage(status) block describing
// what the Fulminate gateway last said about this client's version, and what
// this client's own possession proof last did.
//
// THEY ARE TWO INDEPENDENT PIECES OF STATE and the block keeps them apart on
// purpose. The refusal is the gateway's verdict; the proof is this client's own
// attempt to establish possession. A refusal beside a SUCCESSFUL proof means
// the version is genuinely too old and upgrading is the remedy. A refusal
// beside a FAILED proof means the proof itself is broken and upgrading will not
// help. Collapsing the two would send a user to the wrong remedy.
//
// When NEITHER is set it returns the empty string — no header, no line — so a
// healthy client's status output is byte-identical to what it was before this
// block existed. A short-lived CLI invocation and a daemon in the moments after
// start both legitimately sit in that state.
//
// It deliberately does not touch renderVersionLines: the client/daemon skew
// block is a separate concern with a separate owner, and this block is additive
// beside it.
func renderClientVersionStateLines() string {
	refusal, refused := clientver.CurrentRefusal()
	proof, proved := clientver.LastProof()
	if !refused && !proved {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\nClient version state:\n")
	if refused {
		fmt.Fprintf(&b, "  Refused by the Fulminate gateway (%s): minimum %s, this client %s on %s — run `%s` to upgrade (at %s)\n",
			refusal.Reason,
			blankAsUnknown(refusal.Minimum),
			blankAsUnknown(refusal.ClientVersion),
			blankAsUnknown(refusal.Platform),
			blankAsUnknown(refusal.UpgradeCommand),
			refusal.At.Format(time.RFC3339))
	}
	if proved {
		if proof.OK {
			fmt.Fprintf(&b, "  Version proof: verified at %s (%s on %s)\n",
				proof.At.Format(time.RFC3339), blankAsUnknown(proof.Version), blankAsUnknown(proof.Platform))
		} else {
			fmt.Fprintf(&b, "  Version proof: FAILED at %s: %s\n",
				proof.At.Format(time.RFC3339), blankAsUnknown(proof.Err))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// addClientVersionStateJSON merges the same two records into the status map.
//
// A key is ABSENT rather than empty when its record is unset: an empty object
// reads as "refused with no detail", which is a different and misleading claim
// from "never refused".
func addClientVersionStateJSON(m map[string]any) {
	if refusal, ok := clientver.CurrentRefusal(); ok {
		m["client_version_refusal"] = map[string]any{
			"minimum":         refusal.Minimum,
			"client_version":  refusal.ClientVersion,
			"platform":        refusal.Platform,
			"upgrade_command": refusal.UpgradeCommand,
			"reason":          refusal.Reason,
			"at":              refusal.At.Format(time.RFC3339),
		}
	}
	if proof, ok := clientver.LastProof(); ok {
		entry := map[string]any{
			"ok":       proof.OK,
			"at":       proof.At.Format(time.RFC3339),
			"version":  proof.Version,
			"platform": proof.Platform,
		}
		if proof.Err != "" {
			entry["error"] = proof.Err
		}
		m["client_version_proof"] = entry
	}
}

// blankAsUnknown keeps a rendered line honest when a field the gateway was
// expected to supply arrived empty — "(unknown)" states the absence, while a
// blank would read as an answer.
func blankAsUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
