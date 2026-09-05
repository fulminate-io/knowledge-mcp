// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestRegistration_Rejections asserts Create and Update both reject a record
// colliding with a built-in graph type and a record failing Validate, and that
// neither dispatches a wire call when validation fails. A well-formed novel
// record is accepted.
func TestRegistration_Rejections(t *testing.T) {
	builtinCollide := sampleDef("code") // "code" is a built-in GraphType
	malformed := &knowledgev1.GraphTypeDef{
		Name: "jira",
		Collector: &knowledgev1.CollectorSpec{
			BinaryPath:     "relative/path", // not absolute -> Validate rejects
			ParamTransport: "stdin",
		},
	}
	wellFormedNovel := sampleDef("jira")

	t.Run("create rejects built-in collision", func(t *testing.T) {
		fake := &fakeExec{}
		err := New(fake).Create(context.Background(), builtinCollide)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "collides with a built-in")
		assert.Empty(t, fake.execs, "a rejected registration must not reach the wire")
	})

	t.Run("update rejects built-in collision", func(t *testing.T) {
		fake := &fakeExec{}
		err := New(fake).Update(context.Background(), builtinCollide)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "collides with a built-in")
		assert.Empty(t, fake.execs)
	})

	t.Run("create rejects malformed record", func(t *testing.T) {
		fake := &fakeExec{}
		err := New(fake).Create(context.Background(), malformed)
		require.Error(t, err)
		assert.Empty(t, fake.execs)
	})

	t.Run("update rejects malformed record", func(t *testing.T) {
		fake := &fakeExec{}
		err := New(fake).Update(context.Background(), malformed)
		require.Error(t, err)
		assert.Empty(t, fake.execs)
	})

	t.Run("create accepts well-formed novel record", func(t *testing.T) {
		fake := &fakeExec{}
		fake.queueResp(&knowledgev1.ExecuteResponse{Ids: []string{"jira"}})
		err := New(fake).Create(context.Background(), wellFormedNovel)
		require.NoError(t, err)
		require.Len(t, fake.execs, 1, "an accepted registration dispatches exactly one wire call")
	})
}

// TestIsBuiltinGraphType asserts the collision predicate returns true for every
// built-in and false for a novel name.
func TestIsBuiltinGraphType(t *testing.T) {
	builtins := []string{
		"knowledge", "code", "cloud", "cicd", "practice",
		"linkage", "checks", "logs", "web", "pdf",
	}
	for _, b := range builtins {
		if !kgtypes.IsBuiltinGraphType(b) {
			t.Errorf("IsBuiltinGraphType(%q) = false, want true", b)
		}
	}
	for _, novel := range []string{"jira", "notion", "", "graph_type_def", "Code"} {
		if kgtypes.IsBuiltinGraphType(novel) {
			t.Errorf("IsBuiltinGraphType(%q) = true, want false", novel)
		}
	}
}

// TestValidateRegistration_RefusesRetiredName pins the freed-name half of the
// removal: once IsBuiltinGraphType stops claiming a removed family, its name
// becomes registrable unless something else refuses it.
//
// The defect this alone detects is a REMOVED FAMILY DEGRADING INTO A REGISTERED
// CUSTOM GRAPH. A user registering "transformers" would get a custom type whose
// resolver adopts the leftover ~/.knowledge/transformers/ directory an upgrading
// operator still has on disk — which compiles, and which passes every vocabulary
// test, because from the vocabulary's point of view the name is simply free.
//
// THE NOVEL-NAME LEG IS THE KNOWN-POSITIVE. A validator that refused every
// registration would satisfy the refusal alone, and the registration surface
// would be dead rather than guarded.
func TestValidateRegistration_RefusesRetiredName(t *testing.T) {
	t.Run("a retired built-in name may not be re-registered", func(t *testing.T) {
		fake := &fakeExec{}
		err := New(fake).Create(context.Background(), sampleDef("transformers"))

		require.Error(t, err, "the retired name is refused")
		assert.Contains(t, err.Error(), "RETIRED",
			"the refusal says the name was retired, not that it is unknown")
		assert.Contains(t, err.Error(), "transformers", "and it names the value it rejected")
		assert.NotContains(t, err.Error(), "collides with a built-in",
			"it is NOT the builtin-collision message: the family is gone, so that check no longer fires")
		assert.Empty(t, fake.execs, "a rejected registration must not reach the wire")
	})

	t.Run("update refuses it on the same terms", func(t *testing.T) {
		fake := &fakeExec{}
		err := New(fake).Update(context.Background(), sampleDef("transformers"))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "RETIRED")
		assert.Empty(t, fake.execs, "a rejected update must not reach the wire")
	})

	t.Run("a novel name still registers", func(t *testing.T) {
		// The KNOWN-POSITIVE: same validator, same client, same call shape.
		fake := &fakeExec{}
		err := New(fake).Create(context.Background(), sampleDef("jira"))

		require.NoError(t, err, "registration is guarded, not disabled")
		assert.NotEmpty(t, fake.execs, "an accepted registration DOES reach the wire")
	})

	t.Run("the retired name is not simply unknown to the vocabulary", func(t *testing.T) {
		// Ties the refusal above to the predicate it reads, so a future edit that
		// deletes the map entry fails here rather than silently reopening the name.
		_, retired := kgtypes.RetiredGraphTypeReason("transformers")
		assert.True(t, retired)
		assert.False(t, kgtypes.IsBuiltinGraphType("transformers"),
			"and it is refused BECAUSE it is retired, not because it is still a builtin")
	})
}
