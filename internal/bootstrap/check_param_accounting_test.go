// SPDX-License-Identifier: Apache-2.0

// check_param_accounting_test.go — the manage_checks SEAM, partitioned in BOTH
// directions.
//
// There are two faces onto one corpus-check run: the MCP tool, whose parameters
// are its schema, and `knowledge check run`, whose parameters are a flag set, a
// positional list and a verb. A parameter present on one face and absent from
// the other is not automatically wrong — create has no shell consumer and --port
// has no MCP meaning — but it must be a RECORDED decision rather than a
// discovery made later by a caller.
//
// WHY BOTH DIRECTIONS. A table offering each schema property "a CLI carrier or
// an exemption" is green forever on the day someone adds a CLI flag: the flag
// has no schema property to be classified against and the table never looks at
// it. The reverse direction is what makes the accounting total.
//
// The partition contract is copied from the mutate-schema parity test in package
// tools, which replaced a single static map that stayed green while individual
// arms silently dropped params. Three properties per direction: the union is the
// exact live key set, the classes are pairwise disjoint, and no class names a key
// the live surface does not have.
//
// THIS TEST NEEDS NO SELF-TEST, and it had two that proved nothing. Both
// "every X is classified" loops below read the LIVE surface — the schema off
// ManageChecksToolDef and the flag set off newCheckRunFlagSet — so an
// unclassified addition on either side fails HERE, at the two assertions whose
// message ends "is unclassified" — the schema one at line 132 and the CLI one at
// line 162 as this file stands, and findable by that message if an edit moves
// them. Both were executed by deleting a classification row and watching each
// direction go red, naming the offending key. A control
// that re-implements the classification walk inline proves only that the walk
// can be written twice.
//
// THERE IS A THIRD CALLER FACE AND IT HAS NO ROW IN EITHER DIRECTION, which is a
// decision rather than an omission. The topology dispatcher reaches the same
// analyzer by forwarding the caller's whole Request.Extra map verbatim, so the
// run knob is honored there too. It contributes NO schema property and NO CLI
// input, so it is absent from both key sets by construction and there is nothing
// to classify: a generic map is not a parameter surface. What keeps that face
// honest is the analyzer's own strict parse, which is the single point all three
// faces converge on, and it is driven by
// TestTopologyDispatcher_ForwardsTheIncludeTestsKnob in package tools.

package bootstrap

import (
	"flag"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// The two CLI inputs that are not flags. They are named here rather than derived
// because neither lives in the flag set: ids arrive as fs.Args() and the
// operation is the verb the subcommand dispatches on.
const (
	cliPositionalIDs = "<positional args>"
	cliVerb          = "<verb>"
)

// schemaCarrier maps every manage_checks schema property to the CLI input that
// carries it. A property with no CLI carrier belongs in schemaExempt with a
// justification, never here and never missing from both.
var schemaCarrier = map[string]string{
	"operation":     cliVerb,
	"language":      "language",
	"repo":          "repo",
	"path_prefix":   "path-prefix",
	"ids":           cliPositionalIDs,
	"include_tests": "include-tests",
}

// schemaExempt maps every manage_checks schema property with NO CLI carrier to
// the justification for that absence. An empty justification is a parked param
// rather than a decision, and fails.
var schemaExempt = map[string]string{
	"top_k": "the CLI takes no render cap: it renders every finding and its consumer reads an exit status, " +
		"so a cap would clip the body without moving the verdict. Adding --top-k is a scope decision nobody has made.",
	"format": "the CLI renders one way, through foundation.RenderFindings, AND no manage_checks handler reads format at all — " +
		"a structural census of $X.Format over the five manage_checks files returns zero against a same-run known positive of " +
		"72 such sites elsewhere in package tools. Giving format a reader is outside the change that wrote this row and stays a recorded hole.",

	// create and list have no shell consumer at all: the CLI registers one verb,
	// and mirroring the whole tool would double the surface for no gate. Stated
	// at the verb list in this package's own source.
	"name":             "create only; the CLI has no create verb",
	"summary":          "create only; the CLI has no create verb",
	"description":      "create only; the CLI has no create verb",
	"content":          "create only; the CLI has no create verb",
	"severity":         "create only; the CLI has no create verb",
	"check_type":       "create only; the CLI has no create verb",
	"dsl_pattern":      "create only; the CLI has no create verb",
	"check_where":      "create only; the CLI has no create verb",
	"applies_to_tests": "create only; the CLI has no create verb",
	"fixture_bad":      "create only; the CLI has no create verb",
	"fixture_good":     "create only; the CLI has no create verb",
}

// cliExempt maps every CLI input with NO schema property to its justification.
var cliExempt = map[string]string{
	"port": "transport, not a scan parameter: it names the daemon this process dials for the checks corpus, " +
		"which an MCP caller never chooses because it is already connected.",
}

// liveCLIInputs is every input `knowledge check run` accepts, read off the LIVE
// flag set plus the two structural inputs. Reading the flag set rather than a
// second hand-written list is what makes a newly registered flag fail here.
func liveCLIInputs() []string {
	var f checkRunFlags
	fs, _ := newCheckRunFlagSet(&f)
	out := []string{cliVerb, cliPositionalIDs}
	fs.VisitAll(func(fl *flag.Flag) { out = append(out, fl.Name) })
	sort.Strings(out)
	return out
}

func TestCheckParamAccounting_PartitionsBothSidesOfTheSeam(t *testing.T) {
	schema := tools.ManageChecksToolDef().InputSchema.Properties
	require.NotEmpty(t, schema, "the tool must declare params")
	cli := liveCLIInputs()
	require.NotEmpty(t, cli, "the CLI must accept inputs")

	t.Run("every schema property is classified", func(t *testing.T) {
		for key := range schema {
			_, carried := schemaCarrier[key]
			_, exempt := schemaExempt[key]
			assert.Truef(t, carried || exempt,
				"schema property %q is unclassified — name the CLI input that carries it, or exempt it with a justification", key)
			assert.Falsef(t, carried && exempt,
				"schema property %q is both carried and exempt", key)
		}
	})

	t.Run("no classification names a property the schema does not have", func(t *testing.T) {
		for key := range schemaCarrier {
			assert.Containsf(t, schema, key, "schemaCarrier names %q, absent from the live schema — stale entry", key)
		}
		for key := range schemaExempt {
			assert.Containsf(t, schema, key, "schemaExempt names %q, absent from the live schema — stale entry", key)
		}
	})

	t.Run("every carrier names a CLI input that exists", func(t *testing.T) {
		for key, input := range schemaCarrier {
			assert.Containsf(t, cli, input,
				"schema property %q claims CLI input %q, which the CLI does not accept", key, input)
		}
	})

	t.Run("every CLI input is classified", func(t *testing.T) {
		carried := map[string]bool{}
		for _, input := range schemaCarrier {
			carried[input] = true
		}
		for _, input := range cli {
			_, exempt := cliExempt[input]
			assert.Truef(t, carried[input] || exempt,
				"CLI input %q is unclassified — name the schema property it carries, or exempt it with a justification", input)
			assert.Falsef(t, carried[input] && exempt,
				"CLI input %q is both a carrier and exempt", input)
		}
		for key := range cliExempt {
			assert.Containsf(t, cli, key, "cliExempt names %q, which the CLI does not accept — stale entry", key)
		}
	})

	t.Run("every exemption carries a justification", func(t *testing.T) {
		for key, why := range schemaExempt {
			assert.NotEmptyf(t, why, "schema property %q is exempt with no justification — that is a parked param, not a decision", key)
		}
		for key, why := range cliExempt {
			assert.NotEmptyf(t, why, "CLI input %q is exempt with no justification", key)
		}
	})
}
