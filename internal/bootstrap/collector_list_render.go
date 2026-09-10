// SPDX-License-Identifier: Apache-2.0

// collector_list_render.go — what `knowledge collector list` prints.
//
// IT SHOWS BOTH SCOPES AND MARKS THE WINNER, because precedence is invisible
// otherwise: an operator with the same name in both files has no other way to
// see which one a collect will use.
//
// IT NAMES LEGACY CATALOG FAMILIES, and that column is the migration path. A
// family the server still holds a record for and no file entry names is not
// served by anything — nothing resolves from the catalog — so the operator's
// only signal that it exists at all is this column and the invocation beside it.
//
// A COLUMN THAT COULD NOT BE READ SAYS SO. With no daemon reachable the legacy
// column is unreadable, and an omitted column would read as "there are no legacy
// families", which is the one answer it cannot give.

package bootstrap

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// renderCollectorList writes the two tables: the file-backed entries and the
// legacy catalog families.
func renderCollectorList(w io.Writer, loader collectorconfig.Loader, catalog catalogLister) error {
	entries, err := loader.Entries()
	if err != nil {
		return err
	}
	fmt.Fprint(w, renderEntryTable(loader, entries))
	fmt.Fprint(w, renderLegacySection(context.Background(), entries, catalog))
	return nil
}

// renderEntryTable renders every entry from both scopes, marking the winner
// where a name appears twice.
func renderEntryTable(loader collectorconfig.Loader, entries []collectorconfig.ScopedEntry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "user scope:    %s\n", orNone(loader.UserPath))
	fmt.Fprintf(&sb, "project scope: %s\n\n", orNone(loader.ProjectPath))
	if len(entries) == 0 {
		sb.WriteString("No collectors configured. `knowledge collector add` writes an entry.\n")
		return sb.String()
	}
	winner := map[string]collectorconfig.Scope{}
	for _, se := range entries {
		if _, ok := winner[se.Name]; !ok {
			winner[se.Name] = se.Scope // Entries() yields project before user.
		}
	}
	sb.WriteString("| name | scope | transport | provider | tool | in effect |\n")
	sb.WriteString("|------|-------|-----------|----------|------|-----------|\n")
	for _, se := range entries {
		inEffect := "yes"
		if winner[se.Name] != se.Scope {
			inEffect = fmt.Sprintf("no — the %s entry wins", winner[se.Name])
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s |\n",
			se.Name, se.Scope, se.Entry.Type, entryProvider(se.Entry), se.Entry.Tool, inEffect)
	}
	sb.WriteString(renderUnresolvedSection(entries))
	return sb.String()
}

// renderUnresolvedSection names every entry whose own `${VAR}` references do not
// resolve in THIS process, and says whose environment decides that.
//
// IT IS A REPORT, NOT A VERDICT. This CLI's environment is not the daemon's: an
// entry listed here collects fine when the serving daemon holds the name, and one
// absent from it fails when that daemon does not. What the section is for is the
// operator who reads a refusal on one collect and wants to know which entry
// carries the reference — and it is what keeps a listing from rendering an entry
// as ordinary when a value in it is the literal text `${NAME}`.
func renderUnresolvedSection(entries []collectorconfig.ScopedEntry) string {
	var sb strings.Builder
	for _, se := range entries {
		if se.Unresolved == nil {
			continue
		}
		if sb.Len() == 0 {
			sb.WriteString("\nentries whose environment does not resolve in THIS process " +
				"(the serving daemon's environment is what a collect reads):\n")
		}
		fmt.Fprintf(&sb, "  %s (%s scope): %v\n", se.Name, se.Scope, se.Unresolved)
	}
	return sb.String()
}

// entryProvider renders the entry's connection target for the table.
func entryProvider(e collectorconfig.Entry) string {
	switch e.Type {
	case collectorconfig.TransportStdio:
		return e.Command
	case collectorconfig.TransportHTTP:
		return e.URL
	default:
		return "-"
	}
}

// renderLegacySection reads the server catalog and names every family it holds
// that no entry claims.
func renderLegacySection(ctx context.Context, entries []collectorconfig.ScopedEntry, catalog catalogLister) string {
	var sb strings.Builder
	sb.WriteString("\nlegacy (server-catalog families with no config entry):\n")
	if catalog == nil {
		sb.WriteString("  the legacy column could NOT be read: no catalog reader is wired. This is not a report that there are none.\n")
		return sb.String()
	}
	defs, err := catalog(ctx)
	if err != nil {
		fmt.Fprintf(&sb, "  the legacy column could NOT be read: %v. This is not a report that there are none.\n", err)
		return sb.String()
	}
	claimed := make(map[string]struct{}, len(entries))
	for _, se := range entries {
		claimed[se.Name] = struct{}{}
	}
	names := make([]string, 0, len(defs))
	byName := make(map[string]int, len(defs))
	for i, d := range defs {
		if d.GetName() == "" {
			continue
		}
		if _, ok := claimed[d.GetName()]; ok {
			continue
		}
		names = append(names, d.GetName())
		byName[d.GetName()] = i
	}
	if len(names) == 0 {
		sb.WriteString("  none.\n")
		return sb.String()
	}
	sort.Strings(names)
	for _, name := range names {
		d := defs[byName[name]]
		label, convertible := collectorconfig.LegacyClass(d)
		if !convertible {
			fmt.Fprintf(&sb, "  %s — %s: the record names no tool, so no entry can be synthesized from it. Write one by hand (see the custom collector guide).\n", name, label)
			continue
		}
		fmt.Fprintf(&sb, "  %s — %s. Nothing resolves from the catalog; write the entry:\n    %s\n",
			name, label, collectorconfig.LegacyInvocation(d, collectorconfig.ScopeUser))
		if s := d.GetCollector().GetStdio(); s != nil && len(s.GetEnv()) > 0 {
			fmt.Fprintf(&sb, "    (its env carried NAMES only, so each %s above is yours to fill in)\n", collectorconfig.EnvValuePlaceholder)
		}
	}
	return sb.String()
}

// orNone renders an unresolved scope path as an explicit phrase rather than an
// empty cell, so "this session has no project scope" is readable as a fact.
func orNone(path string) string {
	if path == "" {
		return "(none for this directory)"
	}
	return path
}
