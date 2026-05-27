// SPDX-License-Identifier: Apache-2.0

package cicd

// SummarizeAllowlist enumerates ResourceType strings that are intentionally
// shipped without a registered summarizer. The Phase 8 audit treats these as
// waived (generic fallback is acceptable forever for these types).
//
// Add an entry only with a one-line comment explaining why fallback is OK.
var SummarizeAllowlist = map[string]bool{}
