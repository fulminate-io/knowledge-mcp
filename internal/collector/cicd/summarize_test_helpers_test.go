// SPDX-License-Identifier: Apache-2.0

package cicd

// resetForTesting wipes the summarizer registry between subtests so
// double-Register panics stay deterministic across parallel tests.
func resetForTesting() {
	summarizersMu.Lock()
	defer summarizersMu.Unlock()
	summarizers = make(map[string]SummarizeFunc)
}
