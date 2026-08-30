// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyMessageErr is a non-connect error whose Error() is the empty string — the
// second shape that produced a bare "engine: " body. A named type rather than
// errors.New("") so the render has a concrete type to report.
type emptyMessageErr struct{}

func (emptyMessageErr) Error() string { return "" }

// TestRenderEngineError_NeverEmitsBarePrefix (FAILS-WHEN-ABSENT) drives the two
// error shapes that produced today's bare prefix and asserts the rendered body is
// never just "engine:".
//
// THE RELAY LEGS ARE WHAT KEEP THE FIX FROM BEING A BLANKET REWRITE. An
// implementation that appended diagnostic furniture to every error — including
// the ones that already read well — would fail them.
func TestRenderEngineError_NeverEmitsBarePrefix(t *testing.T) {
	body := func(t *testing.T, err error) string {
		t.Helper()
		res := renderEngineError(err)
		require.NotEmpty(t, res.Content, "an error render always carries a body")
		return res.Content[0].Text
	}

	t.Run("empty connect message names the code", func(t *testing.T) {
		// CodeInternal: outside CodeInvalidArgument/CodeNotFound (relayed verbatim)
		// and outside CodeUnavailable (caught by the ladder above), so it lands in
		// the generic connect arm this leg is about.
		got := body(t, connect.NewError(connect.CodeInternal, errors.New("")))
		assert.Greater(t, len(strings.TrimSpace(got)), len("engine:"),
			"the body must be more than the bare prefix; got %q", got)
		assert.NotEqual(t, "engine:", strings.TrimSpace(got))
		assert.Contains(t, got, connect.CodeInternal.String(),
			"a connect error always carries a CODE even when its message is empty — name it")
	})

	t.Run("empty non-connect error names the type", func(t *testing.T) {
		got := body(t, emptyMessageErr{})
		assert.Greater(t, len(strings.TrimSpace(got)), len("engine:"), "got %q", got)
		assert.Contains(t, got, "emptyMessageErr", "a bare Go error always has a concrete TYPE — name it")
	})

	t.Run("neither invents a cause", func(t *testing.T) {
		// Reporting an inability truthfully is the correct output; guessing
		// "connection reset" from an empty message would be manufacturing a
		// diagnosis. Pinned so a later edit cannot helpfully add one.
		for _, err := range []error{connect.NewError(connect.CodeInternal, errors.New("")), emptyMessageErr{}} {
			got := body(t, err)
			for _, invented := range []string{"connection reset", "timed out", "refused", "network"} {
				assert.NotContainsf(t, strings.ToLower(got), invented,
					"the render must not guess a cause from an empty message; got %q", got)
			}
		}
	})

	t.Run("a real invalid-argument message is relayed bare", func(t *testing.T) {
		got := body(t, connect.NewError(connect.CodeInvalidArgument, errors.New("fields: unsupported key")))
		assert.Equal(t, "fields: unsupported key", got,
			"a validation message is relayed VERBATIM, with no prefix — unchanged by this step")
	})

	t.Run("a real generic message keeps the engine prefix", func(t *testing.T) {
		got := body(t, connect.NewError(connect.CodeInternal, errors.New("segment merge failed")))
		assert.Equal(t, "engine: segment merge failed", got,
			"a non-empty generic message is relayed under the prefix exactly as before")
	})

	t.Run("a real non-connect message keeps the engine prefix", func(t *testing.T) {
		got := body(t, errors.New("dial tcp: bad host"))
		assert.Equal(t, "engine: dial tcp: bad host", got)
	})

	t.Run("the ladder ordering is unbroken", func(t *testing.T) {
		// CodeUnavailable must still reach the local-server-unreachable branch and
		// NOT the generic arm. An emptiness check hoisted above the ladder would
		// swallow this actionable message, which is the hazard the function's
		// header names — and an EMPTY-message CodeUnavailable is exactly the input
		// that would expose such a hoist.
		for _, err := range []error{
			connect.NewError(connect.CodeUnavailable, errors.New("connect: connection refused")),
			connect.NewError(connect.CodeUnavailable, errors.New("")),
		} {
			got := body(t, err)
			assert.Contains(t, got, "local server unreachable", "got %q", got)
			assert.NotContains(t, got, "engine:", "it must not fall into the generic arm")
			assert.NotContains(t, got, "connection refused", "and must not leak the raw transport text")
		}
	})
}
