// SPDX-License-Identifier: Apache-2.0

package foundation

import "encoding/json"

// RenderFindings formats a slice of analyzer Findings as indented JSON — the
// shared render seam every topology query path uses (the dead_code client path
// and the non-dead_code local-analyzer path both marshal their []Finding through
// here). Relocated out of the client engine package so that engine no longer
// imports the topology suite (breaking the engine<->topology import cycle): the
// analyzers now produce foundation.Finding directly and render it here.
func RenderFindings(findings []Finding) (string, error) {
	b, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
