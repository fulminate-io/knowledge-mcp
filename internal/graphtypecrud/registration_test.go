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
		"linkage", "transformers", "logs", "web", "pdf",
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
