// SPDX-License-Identifier: Apache-2.0

// collector_get.go — `knowledge collector get`, and the one rule that makes it
// its own file: WHAT IT PRINTS IS THE FILE'S OWN TEXT.
//
// The loader EXPANDS an entry's `${VAR}` references as it reads it, because a
// collect has to dial what the entry names. A read-back verb must not: an entry
// whose env block carries `"GITHUB_TOKEN": "${GITHUB_TOKEN}"` would otherwise
// print the operator's token on any machine that holds it. So this verb resolves
// the entry through the loader — for precedence, the scope and the path — and
// then re-reads that one entry's own unexpanded text to render.

package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// runCollectorGet prints one entry, naming which scope won.
func runCollectorGet(args []string, w io.Writer) error {
	f, err := parseCollectorFlags("get", args, false)
	if err != nil {
		return err
	}
	if len(f.rest) != 1 {
		return errors.New("collector get: expected exactly one name — `knowledge collector get <name>`")
	}
	loader, err := cliLoader()
	if err != nil {
		return err
	}
	se, found, err := loader.Resolve(f.rest[0])
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("collector get: no entry named %q in either scope", f.rest[0])
	}
	// WHAT IS PRINTED IS THE FILE'S OWN TEXT, UNEXPANDED, and that is a
	// disclosure rule rather than a convenience. A resolved entry's env values
	// carry the operator's secrets: an entry written as `"GITHUB_TOKEN":
	// "${GITHUB_TOKEN}"` would print the token itself to a terminal, a pipe or a
	// pasted transcript on any machine that holds it. `get` answers "what does my
	// file say", so it reads the entry back the way `add` wrote it.
	entry, err := rawEntryFor(se)
	if err != nil {
		return err
	}
	rendered, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("collector get: render %q: %w", se.Name, err)
	}
	fmt.Fprintf(w, "collector %q (%s scope)\nfile: %s\n%s\n", se.Name, se.Scope, se.Path, rendered)
	if se.Unresolved != nil {
		// NAMED, NOT SWALLOWED. The reference does not resolve in THIS process,
		// which is a fact about this shell rather than about the daemon that
		// serves collects — so it is reported beside the entry and does not fail
		// the read.
		fmt.Fprintf(w, "this entry's environment does not resolve in this process: %v\n"+
			"A collect reads the environment the serving daemon runs in, not this one.\n",
			se.Unresolved)
	}
	return nil
}

// rawEntryFor re-reads one entry from its own scoped file WITHOUT expansion.
//
// IT RE-READS RATHER THAN TRUSTING THE RESOLVED ROW because the resolved row is
// expanded on the path that succeeds — the one case where the text and the value
// differ is exactly the case a credential is in it.
func rawEntryFor(se collectorconfig.ScopedEntry) (collectorconfig.Entry, error) {
	raw, err := collectorconfig.ReadRaw(se.Path)
	if err != nil {
		return collectorconfig.Entry{}, fmt.Errorf("collector get: %w", err)
	}
	entry, ok := raw[se.Name]
	if !ok {
		// The two reads disagree, which is a file that changed underneath or a
		// reader that lost the entry; either way it is not something to paper
		// over with the expanded copy.
		return collectorconfig.Entry{}, fmt.Errorf(
			"collector get: %q resolved from %s but is not in that file's own text now",
			se.Name, se.Path)
	}
	return entry, nil
}
