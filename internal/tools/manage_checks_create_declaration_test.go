// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_create_declaration_test.go — authoring a check whose class lives
// in test files, in ONE call.
//
// THE CREATE PATH BUILDS A CHECK'S METADATA TWICE: once as the candidate the
// admission gate runs, once as the node that is persisted. A declaration landing
// in only one of them is silently wrong in a way no single observation catches —
// a read-back of the written node sees the persisted map alone, so the
// candidate-only direction passes it. Each map therefore gets its own
// observation here, plus the key-set comparison that ties them.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
)

// writtenCheckMetadata returns the metadata of the check node the create wrote.
// The check is the LAST node body written: the two fixtures go first, which is
// what makes a check impossible to write without them.
func writtenCheckMetadata(t *testing.T, fake *checksWriteFake) map[string]string {
	t.Helper()
	require.NotEmpty(t, fake.plans, "the create must have written something")
	last := fake.plans[len(fake.plans)-1]
	bodies := last.GetNodeBodies()
	require.Len(t, bodies, 1, "the final write is the check node alone")
	return bodies[0].GetMetadata()
}

func TestManageChecks_CreateCarriesTheDeclarationIntoBothMetadataMaps(t *testing.T) {
	t.Run("declared: the PERSISTED node carries it", func(t *testing.T) {
		fake := &checksWriteFake{}
		args := createChecksArgs()
		args["applies_to_tests"] = true
		res := runChecksCreate(t, fake, args)
		require.False(t, res.IsError, "the create must succeed: %s", res.Content[0].Text)
		assert.Equal(t, "true", writtenCheckMetadata(t, fake)[corpus.MetaAppliesToTests],
			"a declaration the caller wrote must reach the node a later run reads")
	})

	t.Run("declared: the CANDIDATE the gate ran carries it", func(t *testing.T) {
		// THE DIRECTION THE READ-BACK CANNOT SEE. A declaration present only in
		// the persisted map leaves the gate validating a check that is not the
		// one written — and the read-back above passes anyway.
		c, err := parseCandidateCheck(manageChecksArgs{
			Name: "n", Language: "go", Severity: "warning",
			CheckType: string(corpus.CheckAstPattern), DSLPattern: "defer $X.Close()",
			AppliesToTests: true,
		})
		require.NoError(t, err)
		assert.True(t, c.AppliesToTests,
			"the candidate handed to the admission gate must be the check that gets written")
	})

	t.Run("omitted: neither map carries the key", func(t *testing.T) {
		fake := &checksWriteFake{}
		res := runChecksCreate(t, fake, createChecksArgs())
		require.False(t, res.IsError, "the create must succeed: %s", res.Content[0].Text)
		_, present := writtenCheckMetadata(t, fake)[corpus.MetaAppliesToTests]
		assert.False(t, present,
			"absent means false in the contract; writing the key anyway invents a second spelling of the default")

		c, err := parseCandidateCheck(manageChecksArgs{
			Name: "n", Language: "go", Severity: "warning",
			CheckType: string(corpus.CheckAstPattern), DSLPattern: "defer $X.Close()",
		})
		require.NoError(t, err)
		assert.False(t, c.AppliesToTests)
	})
}

// contractKeysOfCheck projects the corpus.Check the ADMISSION GATE was handed
// back onto the contract key set that produced it. It is how the candidate side
// of the comparison below is read without asking the create path to hand a test
// its internal map: the Check is what parseCandidateCheck actually returns, and
// a key the candidate map omitted is a field the contract left unset.
func contractKeysOfCheck(c corpus.Check) []string {
	var out []string
	add := func(present bool, key string) {
		if present {
			out = append(out, key)
		}
	}
	add(c.Type != "", corpus.MetaCheckType)
	add(c.Severity != "", corpus.MetaSeverity)
	add(c.Language != "", corpus.MetaLanguage)
	add(c.Pattern != "", corpus.MetaDSLPattern)
	add(len(c.Where) > 0, corpus.MetaCheckWhere)
	add(c.FixtureBad != "", corpus.MetaFixtureBad)
	add(c.FixtureGood != "", corpus.MetaFixtureGood)
	add(c.AppliesToTests, corpus.MetaAppliesToTests)
	return out
}

// TestManageChecks_CreateMetadataMapsHaveOneKeySet ties the check the admission
// gate VALIDATED to the check that was WRITTEN.
//
// THE TWO SIDES ARE TWO INDEPENDENT PRODUCTIONS, and that is the whole value of
// the test. The candidate side comes from parseCandidateCheck, read back through
// the corpus.Check the contract returned; the persisted side comes from a real
// create driven through the write fake. Comparing one constructor's output with
// itself would have no reachable failing input at all, whatever the arguments
// differ by, because the key set does not depend on them.
//
// IT COMPARES KEY SETS, NEVER VALUES. The two legitimately differ in value: the
// candidate carries placeholder fixture bindings and the written node carries
// the ids the fixtures were actually written under. That difference is asserted
// separately below, so the comparison is known not to be a map against itself.
func TestManageChecks_CreateMetadataMapsHaveOneKeySet(t *testing.T) {
	for _, declared := range []bool{false, true} {
		name := "undeclared"
		if declared {
			name = "declared"
		}
		t.Run(name, func(t *testing.T) {
			args := createChecksArgs()
			if declared {
				args["applies_to_tests"] = true
			}

			// PERSISTED: a real create through the write fake.
			fake := &checksWriteFake{}
			res := runChecksCreate(t, fake, args)
			require.False(t, res.IsError, "the create must succeed: %s", res.Content[0].Text)
			persisted := writtenCheckMetadata(t, fake)

			// CANDIDATE: the same caller input decoded the way the interceptor
			// decodes it, then run through the real candidate path.
			raw, err := json.Marshal(args)
			require.NoError(t, err)
			var a manageChecksArgs
			require.NoError(t, json.Unmarshal(raw, &a))
			candidate, err := parseCandidateCheck(a)
			require.NoError(t, err)

			persistedKeys := make([]string, 0, len(persisted))
			for k := range persisted {
				persistedKeys = append(persistedKeys, k)
			}
			assert.ElementsMatch(t, contractKeysOfCheck(candidate), persistedKeys,
				"the check the gate validated and the check that was written must be the same check")

			// THE CONTROL that keeps the comparison from being trivially true:
			// the two productions DO differ in value on the fixture bindings.
			assert.Equal(t, pendingFixtureBad, candidateFixtureBad(t, a),
				"the candidate carries the placeholder binding")
			assert.NotEqual(t, pendingFixtureBad, persisted[corpus.MetaFixtureBad],
				"and the written node carries the id the fixture was written under")
		})
	}
}

// candidateFixtureBad reads the bad-fixture binding off the candidate path.
func candidateFixtureBad(t *testing.T, a manageChecksArgs) string {
	t.Helper()
	c, err := parseCandidateCheck(a)
	require.NoError(t, err)
	return c.FixtureBad
}
