// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expand_test.go — the expansion matrix: five expanding positions times three
// input classes, plus the bare form and the four literal positions.
//
// THE MATRIX IS WRITTEN AS A MATRIX rather than as five happy-path rows because
// a silent drop in ONE arm is exactly the defect it exists to catch: a loader
// that expanded `command` and forgot `headers` passes every test that only looks
// at the first.

// envFrom builds a lookup over a fixed map, so no row depends on the process
// environment and a "not set" row means it.
func envFrom(m map[string]string) varLookup {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

// arm names one expanding position and how to write a value into it and read the
// expanded result back out.
type arm struct {
	name  string
	build func(value string) Entry
	read  func(e Entry) string
}

// expandingArms are the five positions the contract expands.
func expandingArms() []arm {
	stdio := func(mutate func(*Entry)) Entry {
		e := Entry{Type: TransportStdio, Command: "/usr/local/bin/p", Tool: "collect"}
		mutate(&e)
		return e
	}
	httpEntry := func(mutate func(*Entry)) Entry {
		e := Entry{Type: TransportHTTP, URL: "https://c.example/mcp", Tool: "collect"}
		mutate(&e)
		return e
	}
	return []arm{
		{
			"command",
			func(v string) Entry { return stdio(func(e *Entry) { e.Command = v }) },
			func(e Entry) string { return e.Command },
		},
		{
			"args[0]",
			func(v string) Entry { return stdio(func(e *Entry) { e.Args = []string{v} }) },
			func(e Entry) string { return e.Args[0] },
		},
		{
			"env value",
			func(v string) Entry { return stdio(func(e *Entry) { e.Env = map[string]string{"TOKEN": v} }) },
			func(e Entry) string { return e.Env["TOKEN"] },
		},
		{
			"url",
			func(v string) Entry { return httpEntry(func(e *Entry) { e.URL = v }) },
			func(e Entry) string { return e.URL },
		},
		{
			"headers value",
			func(v string) Entry {
				return httpEntry(func(e *Entry) { e.Headers = map[string]string{"Authorization": v} })
			},
			func(e Entry) string { return e.Headers["Authorization"] },
		},
	}
}

// TestExpandEntry_Matrix drives every arm through every input class.
func TestExpandEntry_Matrix(t *testing.T) {
	lookup := envFrom(map[string]string{"SET_VAR": "resolved-value"})
	for _, a := range expandingArms() {
		t.Run(a.name+"/the variable is set", func(t *testing.T) {
			got, err := expandEntry(lookup, "/scratch/collectors.json", "tickets", a.build("${SET_VAR}"))
			require.NoError(t, err)
			assert.Equal(t, "resolved-value", a.read(got))
		})

		t.Run(a.name+"/unset with a default", func(t *testing.T) {
			got, err := expandEntry(lookup, "/scratch/collectors.json", "tickets", a.build("${MISSING_VAR:-fallback}"))
			require.NoError(t, err)
			assert.Equal(t, "fallback", a.read(got))
		})

		t.Run(a.name+"/unset with no default is an ERROR", func(t *testing.T) {
			_, err := expandEntry(lookup, "/scratch/collectors.json", "tickets", a.build("${MISSING_VAR}"))
			require.Error(t, err,
				"an unset variable with no default must be refused: an empty substitution fails far away from the file that caused it")
			assert.Contains(t, err.Error(), "/scratch/collectors.json")
			assert.Contains(t, err.Error(), "tickets")
			assert.Contains(t, err.Error(), "MISSING_VAR")
		})
	}
}

// TestExpandEntry_SubstitutionIsInline pins that a reference inside a longer
// string substitutes in place rather than replacing the whole value, which is
// how a bearer token and a URL path are actually written.
func TestExpandEntry_SubstitutionIsInline(t *testing.T) {
	lookup := envFrom(map[string]string{"TOKEN": "abc123", "HOST": "c.example"})
	got, err := expandEntry(lookup, "/f", "remote", Entry{
		Type: TransportHTTP, Tool: "collect",
		URL:     "https://${HOST}/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
	})
	require.NoError(t, err)
	assert.Equal(t, "https://c.example/mcp", got.URL)
	assert.Equal(t, "Bearer abc123", got.Headers["Authorization"])
}

// TestExpandEntry_BareDollarFormIsExpanded pins the acceptance as DELIBERATE.
// os.Expand's own scan takes `$VAR`, Claude's documentation does not list it, and
// refusing it would need a pre-scan that also refuses a literal `$` in a
// password. The row exists so the behavior is a decision rather than an accident
// of the primitive.
func TestExpandEntry_BareDollarFormIsExpanded(t *testing.T) {
	lookup := envFrom(map[string]string{"SET_VAR": "resolved-value"})
	for _, a := range expandingArms() {
		t.Run(a.name, func(t *testing.T) {
			got, err := expandEntry(lookup, "/f", "tickets", a.build("$SET_VAR"))
			require.NoError(t, err)
			assert.Equal(t, "resolved-value", a.read(got))
		})
	}
}

// TestExpandEntry_BareDollarFormUnsetIsRefusedByName is the OTHER HALF of the
// row above, and it is the half a downstream gate rests on.
//
// WHAT DEPENDS ON IT. The installer suite refuses an unbraced `$VAR` inside a
// shipped worked entry, and the ground it gives for doing so is this loader's
// behaviour: an unset unbraced reference refuses the WHOLE scoped config file,
// taking every other collector in that scope with it. That ground was pinned by
// nothing. The sibling above drives `$SET_VAR` with the name SET, so it observes
// expansion and never refusal; a change to the expansion primitive, or a pre-scan
// added ahead of it, would leave the documentation gate refusing a shape on a
// rule nothing checked.
//
// IT ASSERTS THE NAME IS IN THE MESSAGE, not merely that an error came back: an
// operator whose whole file was refused can only act if the refusal says which
// variable did it.
func TestExpandEntry_BareDollarFormUnsetIsRefusedByName(t *testing.T) {
	lookup := envFrom(map[string]string{})
	for _, a := range expandingArms() {
		t.Run(a.name, func(t *testing.T) {
			_, err := expandEntry(lookup, "/scratch/collectors.json", "tickets", a.build("$UNSET_VAR"))
			require.Error(t, err,
				"an unset unbraced reference must refuse the file; the installer suite refuses this spelling in a "+
					"shipped worked entry on exactly this ground")
			assert.Contains(t, err.Error(), "UNSET_VAR",
				"the refusal does not name the variable, so an operator whose file was refused cannot act on it")
		})
	}
}

// TestExpandEntry_LiteralPositionsAreNotExpanded is the negative half, and it is
// CONTROLLED: every variable it writes into a literal position IS SET in the
// lookup, so an unexpanded result is evidence the position is literal rather
// than evidence the variable was missing.
func TestExpandEntry_LiteralPositionsAreNotExpanded(t *testing.T) {
	lookup := envFrom(map[string]string{"VAR": "SUBSTITUTED"})

	t.Run("the entry name", func(t *testing.T) {
		out, err := parseFile(lookup, "/f", []byte(
			`{"collectors":{"${VAR}":{"type":"stdio","command":"/p","tool":"collect"}}}`))
		require.NoError(t, err)
		_, ok := out["${VAR}"]
		assert.True(t, ok, "the entry name is the graph family and stays verbatim; got %v", keysOf(out))
	})

	t.Run("tool, env keys and header keys", func(t *testing.T) {
		got, err := expandEntry(lookup, "/f", "tickets", Entry{
			Type: TransportHTTP, URL: "https://c.example/mcp", Tool: "${VAR}",
			Headers: map[string]string{"${VAR}": "v"},
		})
		require.NoError(t, err)
		assert.Equal(t, "${VAR}", got.Tool, "tool is a literal position")
		_, ok := got.Headers["${VAR}"]
		assert.True(t, ok, "a header KEY is a literal position: a key computed from the environment makes the file unreadable")

		gotStdio, err := expandEntry(lookup, "/f", "tickets", Entry{
			Type: TransportStdio, Command: "/p", Tool: "collect",
			Env: map[string]string{"${VAR}": "v"},
		})
		require.NoError(t, err)
		_, ok = gotStdio.Env["${VAR}"]
		assert.True(t, ok, "an env KEY is a literal position")
	})

	t.Run("CONTROL: the same variable DOES expand in a value position", func(t *testing.T) {
		got, err := expandEntry(lookup, "/f", "tickets", Entry{
			Type: TransportStdio, Command: "/p", Tool: "collect",
			Env: map[string]string{"NAME": "${VAR}"},
		})
		require.NoError(t, err)
		assert.Equal(t, "SUBSTITUTED", got.Env["NAME"],
			"without this the assertions above are satisfied by an expander that does nothing at all")
	})
}

// TestExpandEntry_ReportsTheFirstFailure pins the determinism of the message: a
// file with two unresolvable references always names the same one first, so an
// operator fixing errors one at a time makes progress.
func TestExpandEntry_ReportsTheFirstFailure(t *testing.T) {
	_, err := expandEntry(envFrom(nil), "/f", "tickets", Entry{
		Type: TransportStdio, Tool: "collect",
		Command: "${FIRST_MISSING}/${SECOND_MISSING}",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FIRST_MISSING")
	assert.NotContains(t, err.Error(), "SECOND_MISSING")
}

// keysOf renders a map's keys for a failure message. It is generic over the
// value because the loader's own rows carry an entry plus that entry's own
// refusal, while a caller-built map holds bare entries.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, fmt.Sprintf("%q", k))
	}
	return out
}
