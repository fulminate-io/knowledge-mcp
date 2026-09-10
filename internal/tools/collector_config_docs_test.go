// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collector_config_docs_test.go — the guide DOCUMENTS THE CONTRACT, asserted by
// execution rather than by a human read.
//
// WHY A TEST AND NOT A REVIEW. A prose requirement with no executable
// observation is how a guide falls behind its code: the page keeps describing a
// contract nobody implements any more, and the only signal is a user following
// it. This package already fences these pages for exactly that reason — the
// sibling census pins that no page carries the RETIRED vocabulary, and this pins
// that the CURRENT one is present.
//
// IT READS THROUGH THE SAME SYMLINK the sibling census uses, which is what puts
// the page in this module's test-cache key; reading it by a relative path outside
// the module would serve a cached pass after a docs-only edit.

// customCollectorGuide reads the shipped guide through the fenced link.
func customCollectorGuide(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(docsLinkDir, "tools", "custom_collector.md"))
	require.NoError(t, err)
	require.NotEmpty(t, body)
	return string(body)
}

// TestCustomCollectorGuide_DocumentsTheConfigContract is R9's observation.
//
// EACH ROW NAMES WHAT A READER WOULD BE MISSING, because a bare "the guide must
// mention X" failure sends an author to add the word rather than the section.
func TestCustomCollectorGuide_DocumentsTheConfigContract(t *testing.T) {
	page := customCollectorGuide(t)

	for _, tc := range []struct{ want, missing string }{
		{`"collectors"`, "the container key of the config file"},
		{collectorconfig.FileName, "the file's name"},
		{"~/.knowledge/" + collectorconfig.FileName, "the user-scope path"},
		{"<repo root>/.knowledge/" + collectorconfig.FileName, "the project-scope path"},
		{"no field is merged across", "the precedence rule's no-merge clause"},
		// THE DIRECTION, not just the no-merge clause. The guide is the operator's
		// only statement of WHICH of two entries wins, and a sentence that rotted
		// to the wrong order would send them to the file that is being ignored.
		// Measured: flipping the guide to "user, then project" left every test
		// green before this row.
		{"project, then user", "the precedence order"},
		{"knowledge collector add", "the add verb"},
		{"knowledge collector list", "the list verb"},
		{"knowledge collector get", "the get verb"},
		{"knowledge collector remove", "the remove verb"},
		{"-s user|project", "the scope flag"},
		{"-e KEY=VALUE", "the repeatable env flag"},
		{"-H 'Name: value'", "the repeatable header flag"},
		{"--tool", "the flag naming the tool to call"},
		{"${VAR}", "the expansion form"},
		{"${VAR:-default}", "the defaulted expansion form"},
		{"$VAR", "the bare expansion form"},
		{"unset variable with no default is an ERROR", "what an unresolvable reference does"},
		{"LITERAL", "which positions are not expanded"},
		{"legacy", "the disposition of a catalog family with no entry"},
		{"nothing resolves from", "that the catalog is never a fallback"},
		{"no daemon", "which verbs need a running server"},
		{"dials the provider before writing", "when the contract check runs"},
		{"FIRST collect", "when a hand-edited entry is dialed"},
		{`"operation": "delete"`, "how to remove a stale legacy catalog record"},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			assert.Contains(t, page, tc.want,
				"the guide does not document %s, so a reader cannot find it", tc.missing)
		})
	}
}

// TestCustomCollectorGuide_CarriesOneWorkedExamplePerTransport pins that both
// transports are shown end to end. A guide showing one is a guide whose other
// transport is discovered by trial.
func TestCustomCollectorGuide_CarriesOneWorkedExamplePerTransport(t *testing.T) {
	page := customCollectorGuide(t)

	assert.Contains(t, page, `"type": "stdio"`, "a worked stdio entry")
	assert.Contains(t, page, `"type": "http"`, "a worked http entry")
	assert.Contains(t, page, `"tool":`, "both examples must name the tool field, which is the one deliberate difference from Claude's config")
	assert.Contains(t, page, "-t http", "an http `knowledge collector add` invocation")

	// THE COMMAND AFTER THE TERMINATOR IS ABSOLUTE, and the literal here moved
	// with the guide rather than pinning the shape the guide used to show. The
	// daemon resolves it with LookPath against ITS OWN PATH, so a bare name in a
	// worked invocation registers cleanly and fails at the first collect; the
	// gate for that class over every fence is
	// TestGuideEntries_GiveCommandAnAbsolutePath, and this row keeps the
	// terminator example itself from regressing.
	assert.Contains(t, page, "-- /usr/local/bin/ticket-mcp",
		"a stdio invocation with an ABSOLUTE command after the terminator")
}

// TestCustomCollectorGuide_DescribesTheEnvBlockAsValues is the row that catches
// the retired contract's most quotable sentence surviving a rewrite: the block
// used to be NAMES ONLY, and it is now name-to-value pairs that ARE the child's
// whole environment. A page still saying "allowlist of variable NAMES, never
// values" would teach an author to write an entry this client refuses.
func TestCustomCollectorGuide_DescribesTheEnvBlockAsValues(t *testing.T) {
	page := customCollectorGuide(t)

	assert.Contains(t, page, "complete environment",
		"the guide must say the env block IS the child's whole environment")
	assert.Contains(t, page, "the block's value",
		"and that a name in the block arrives carrying the ENTRY's value")

	for _, retired := range []string{
		"allowlist of variable NAMES",
		"never put a secret in the registration record",
		"is refused at registration",
		"unauthenticated in v1",
		"no auth may be configured",
	} {
		assert.NotContains(t, strings.ToLower(page), strings.ToLower(retired),
			"the guide still carries a retired sentence: %q", retired)
	}
}
