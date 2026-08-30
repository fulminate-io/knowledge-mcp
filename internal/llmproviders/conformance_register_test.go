// SPDX-License-Identifier: Apache-2.0

// This file is the ONLY part of the conformance suite that lives in the
// EXTERNAL test package (llmproviders_test). Its sole job is to blank-import the
// five provider sub-packages so their init() functions register their factories
// into the process-global llm registry before the in-package conformance suite
// (package llmproviders, conformance_test.go) calls llm.NewClient /
// llm.ListProviders.
//
// Why the split: llmproviders is provider-neutral by construction — it names no
// concrete provider package, in test files or otherwise, and reaches every
// provider through the llm registry. An EXTERNAL test package carries the blank
// imports without putting that dependency on the package itself (the same route
// llm/integration_test.go uses for the identical registration set), so the
// registrations live here while the suite that must reach the unexported
// parseSummariesContent stays in-package.
//
// The registration set here MUST equal llm/integration_test.go's set; the
// in-package TestConformance_CoversEveryRegisteredProvider enforces table ==
// llm.ListProviders() in both directions, which is the live check on this set.
package llmproviders_test

import (
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/anthropic"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/claudecli"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/codexcli"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/gemini"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/openai"
)
