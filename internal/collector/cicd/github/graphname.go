// SPDX-License-Identifier: Apache-2.0

package github

// GraphName is THE ONE DEFINITION of the graph name a GitHub collect lands
// under: the "github-" prefix joined to the org/workspace id the collect was
// handed.
//
// TWO PRODUCTION CALLERS READ THIS ONE ANSWER. GitHubCollector.Collect fills
// collectorwire.CollectResult.GraphName from it, so it is the name the graph is
// actually created under; and the collect dispatch calls it to know that name
// BEFORE the walk, which is what lets the in-flight gate record an identity a
// registered collector can match.
//
// IT IS A FUNCTION RATHER THAN AN INLINE CONCATENATION because the dispatch
// cannot reach an expression that lives inside the collector's return statement.
// A second inline concatenation would be a SECOND definition of the same
// identity, and the two would drift in silence: a predicted name matching no
// collector's name gates nothing, raises no error and writes no log.
func GraphName(id string) string { return "github-" + id }
