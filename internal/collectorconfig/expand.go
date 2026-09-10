// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"fmt"
	"os"
	"strings"
)

// expand.go — the `${VAR}` / `${VAR:-default}` substitution an entry's VALUES
// carry, and the four positions that stay literal.
//
// WHY IT EXISTS: it is how an operator keeps a credential out of a file that
// lives at the root of a shared repository while still naming the variable the
// entry needs. It is the OPERATOR referencing THEIR OWN environment in THEIR OWN
// entry — the daemon still originates nothing and passes nothing of its own,
// which is the contract this whole package implements.
//
// WHERE IT APPLIES: `command`, every element of `args`, every VALUE of `env`,
// `url`, and every VALUE of `headers`.
//
// WHERE IT DOES NOT, AND WHY THAT IS A RULE RATHER THAN AN OMISSION: the entry
// NAME, `tool`, every KEY of `env` and every KEY of `headers` are literal. A key
// computed from the environment makes the file's own shape unreadable — no
// reader could tell which variables an entry supplies without reproducing the
// launcher's environment first.

// varLookup resolves one variable name. os.LookupEnv in production; a test
// supplies its own so no suite depends on the process environment.
type varLookup func(name string) (string, bool)

// defaultVarLookup is the production resolver.
func defaultVarLookup(name string) (string, bool) { return os.LookupEnv(name) }

// defaultSeparator is the `:-` in `${VAR:-default}`, the one form Claude's own
// config documents beside the bare `${VAR}`.
const defaultSeparator = ":-"

// expandValue substitutes environment references in one field's value.
//
// AN UNSET VARIABLE WITH NO DEFAULT IS AN ERROR, never an empty string. An empty
// substitution is the failure this whole function exists to make impossible: a
// command that expanded to nothing, or an Authorization header that expanded to
// the word "Bearer" and no token, fails somewhere far away from the file that
// caused it.
//
// THE MAPPING FUNCTION CANNOT RETURN AN ERROR, which is the one mechanical
// awkwardness here and is worth stating plainly rather than hiding: os.Expand
// takes a func(string) string, so the mapper records the first failure into a
// captured variable and this function reads it after the call. Every reference is
// still visited, so the recorded failure is the FIRST one in the string.
//
// THE BARE `$VAR` FORM IS ACCEPTED TOO. os.Expand's own scan takes it, Claude's
// documentation does not list it, and refusing it would need a pre-scan that
// also refuses a literal `$` in a password. Accepting it is the deliberate
// choice; there is a test that pins it as one.
func expandValue(lookup varLookup, path, entry, field, value string) (string, error) {
	if lookup == nil {
		lookup = defaultVarLookup
	}
	var firstErr error
	out := os.Expand(value, func(ref string) string {
		name, fallback, hasDefault := strings.Cut(ref, defaultSeparator)
		if v, ok := lookup(name); ok {
			return v
		}
		if hasDefault {
			return fallback
		}
		if firstErr == nil {
			firstErr = fmt.Errorf(
				"%s: collector %q: %s references ${%s}, which is not set in the environment and carries no ${%s:-default}",
				path, entry, field, name, name)
		}
		return ""
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// ExpandForDial expands one entry exactly as a load would, so the write path
// dials WHAT A COLLECT WILL DIAL rather than the unexpanded text. The entry it
// returns is for dialing only: `knowledge collector add` writes the UNEXPANDED
// entry to the file, which is the whole point of the reference.
func ExpandForDial(path, name string, e Entry) (Entry, error) {
	return expandEntry(nil, path, name, e)
}

// ValidateEntry is validateEntry for a caller holding an entry it built rather
// than loaded — the CLI's add path, which must refuse a malformed entry BEFORE
// writing it rather than at the next load.
func ValidateEntry(path, name string, e Entry) error { return validateEntry(path, name, e) }

// expandEntry returns a copy of e with every expanding position substituted. The
// literal positions are copied verbatim, and the copy is deep enough that the
// caller's decoded entry is never mutated in place — a half-expanded entry left
// behind by a failure is exactly the state a later reader could not interpret.
func expandEntry(lookup varLookup, path, name string, e Entry) (Entry, error) {
	out := e
	var err error

	if out.Command, err = expandValue(lookup, path, name, "command", e.Command); err != nil {
		return Entry{}, err
	}
	if out.URL, err = expandValue(lookup, path, name, "url", e.URL); err != nil {
		return Entry{}, err
	}
	if len(e.Args) > 0 {
		out.Args = make([]string, len(e.Args))
		for i, a := range e.Args {
			if out.Args[i], err = expandValue(lookup, path, name, fmt.Sprintf("args[%d]", i), a); err != nil {
				return Entry{}, err
			}
		}
	}
	if out.Env, err = expandMap(lookup, path, name, "env", e.Env); err != nil {
		return Entry{}, err
	}
	if out.Headers, err = expandMap(lookup, path, name, "headers", e.Headers); err != nil {
		return Entry{}, err
	}
	return out, nil
}

// expandMap expands every VALUE of a name→value block, leaving every KEY exactly
// as written. The keys are walked in SORTED order so a file with two failing
// values always reports the same one first.
func expandMap(lookup varLookup, path, entry, field string, in map[string]string) (map[string]string, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for _, k := range sortedKeys(in) {
		v, err := expandValue(lookup, path, entry, fmt.Sprintf("%s[%q]", field, k), in[k])
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}
