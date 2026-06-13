// SPDX-License-Identifier: Apache-2.0

// This file is the ONLY part of the conformance suite that lives in the
// EXTERNAL test package (llmproviders_test). Its sole job is to blank-import the
// five provider sub-packages so their init() functions register their factories
// into the process-global llm registry before the in-package conformance suite
// (package llmproviders, conformance_test.go) calls llm.NewClient /
// llm.ListProviders.
//
// Why the split: claude-cli and codex-cli transitively import llmproviders (via
// graphclient → hivemonitor → the supervisor handler), so importing them from an
// IN-PACKAGE test file would be an import cycle. An EXTERNAL test package may
// import packages that depend on the package under test (Go special-cases the
// `_test` package exactly as llm/integration_test.go relies on for the same
// registration imports), so the blank imports live here while the suite that
// must reach the unexported parseSummariesContent stays in-package.
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
