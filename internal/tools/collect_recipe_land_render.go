// SPDX-License-Identifier: Apache-2.0

// collect_recipe_land_render.go — the landing response.
//
// IT IS A SIBLING RENDER, NOT A WIDENING OF renderExtract. The two answer
// different questions: an extract reports what it would show you, a landing
// reports what it wrote. Folding them would give one header two meanings, and the
// counter a reader most needs — how many nodes are now in the graph — has no
// place in an extract's row accounting.
//
// IT FOLLOWS renderExtract's DISCLOSURE DISCIPLINE, which is the part worth
// copying: every counter is in the header, so a run that MATCHED nothing is
// distinguishable from one that matched rows and landed none, and a twin is named
// with its version rather than counted anonymously.

package tools

import (
	"fmt"
	"sort"
	"strings"
)

// renderLanding renders a completed landing.
func renderLanding(a collectArgs, out landingOutcome) string {
	hubState := "reused"
	if out.hubCreated {
		hubState = "created"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb,
		"landed: recipe=%s source=%s/%s hub=%s(%s) nodes=%d matched=%d twins=%d\n",
		recipeLabel(a), a.Type, a.ID, hubState, out.hubID, out.landed, out.matched, len(out.twins))

	// THE TWINS ARE NAMED WITH THEIR VERSIONS, sorted, because "twins=3" tells a
	// reader that something collided and not what. The sort is required rather
	// than cosmetic: the emitted order is a map-derived walk upstream, and a
	// landing response exists to be compared across runs.
	if len(out.twins) > 0 {
		rows := make([]string, 0, len(out.twins))
		for _, tw := range out.twins {
			name := tw.name
			if name == "" {
				name = "(unnamed)"
			}
			rows = append(rows, fmt.Sprintf("  ~ v%d  %s  %s\n", tw.version, tw.id, name))
		}
		sort.Strings(rows)
		sb.WriteString("versioned twins (the resident row is untouched and retained):\n")
		for _, r := range rows {
			sb.WriteString(r)
		}
	}

	// THE TWO ZEROS SAY DIFFERENT THINGS, and saying so is the whole reason the
	// header carries `matched` beside `nodes`. A body that matched no rows and a
	// body whose every row was skipped for an empty identity both land nothing;
	// only the counters tell them apart, and a caller that cannot tell them apart
	// edits the wrong half of its recipe.
	if out.landed == 0 {
		if out.matched == 0 {
			sb.WriteString("nothing landed: the recipe body matched no rows in this source graph.\n")
		} else {
			sb.WriteString("nothing landed: rows matched but none survived emission — see the skipped counter on an extract run.\n")
		}
	}
	return sb.String()
}
