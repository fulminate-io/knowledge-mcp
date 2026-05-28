// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// renderTicketHeader writes the status/external_id/external_url/archived/
// priority/labels/description metadata header + the `ID:` line to sb.
// Verbatim port of cmd/knowledge-server/tools/tools_assemble_containers.go:102.
//
// Render shape is intentionally backend-agnostic: no adapter name (no
// "Linear", "Jira", etc.) appears in the output — the `external_url` and
// `external_id` keys carry whichever backend produced them, and the user
// reads the deeplink to know which UI to open. Archived flag is shown
// only when explicitly "true" (default "false" stays silent so the
// header doesn't grow noisier in the common active-ticket case).
func renderTicketHeader(node *knowledgev1.Node, sb *strings.Builder) {
	if node.Status != "" {
		fmt.Fprintf(sb, "**Status:** %s\n", node.Status)
	}
	if v := kgtypes.Value(node, "external_id"); v != "" {
		fmt.Fprintf(sb, "**External ID:** %s\n", v)
	}
	if v := kgtypes.Value(node, "external_url"); v != "" {
		fmt.Fprintf(sb, "**URL:** %s\n", v)
	}
	if kgtypes.Value(node, "external_archived") == "true" {
		fmt.Fprintf(sb, "**Archived:** true\n")
	}
	if v := kgtypes.Value(node, "priority"); v != "" {
		fmt.Fprintf(sb, "**Priority:** %s\n", v)
	}
	if v := kgtypes.Value(node, "labels"); v != "" {
		fmt.Fprintf(sb, "**Labels:** %s\n", v)
	}
	if node.Description != "" {
		fmt.Fprintf(sb, "\n%s\n", node.Description)
	}
	fmt.Fprintf(sb, "\nID: %s\n", node.Id)
}

// renderProjectHeader writes the project's status/external_id/URL/archived/
// description block + the `ID:` line. Mirrors renderTicketHeader but
// collapses external_id + external_url to a single line when they're equal
// — a project (Linear-style) has no human-readable identifier distinct
// from the deeplink, so BuildProjectNode sets external_id = ref.URL. The
// collapsed render avoids duplicating the URL on two adjacent lines.
//
// Verbatim port of cmd/knowledge-server/tools/tools_assemble_containers.go:138.
//
// Render shape is intentionally backend-agnostic: the adapter name
// ("linear", "jira", ...) is in the `backend` metadata key but never
// surfaced in this header. The user reads the deeplink to know which UI
// to open.
func renderProjectHeader(node *knowledgev1.Node, sb *strings.Builder) {
	if node.Status != "" {
		fmt.Fprintf(sb, "**Status:** %s\n", node.Status)
	}
	extID := kgtypes.Value(node, "external_id")
	extURL := kgtypes.Value(node, "external_url")
	switch {
	case extID != "" && extID == extURL:
		// Collapsed: project external_id == URL (BuildProjectNode shape).
		fmt.Fprintf(sb, "**External ID / URL:** %s\n", extURL)
	case extID != "":
		fmt.Fprintf(sb, "**External ID:** %s\n", extID)
		if extURL != "" {
			fmt.Fprintf(sb, "**URL:** %s\n", extURL)
		}
	case extURL != "":
		// Fallback: external_id empty but URL present.
		fmt.Fprintf(sb, "**URL:** %s\n", extURL)
	}
	if kgtypes.Value(node, "external_archived") == "true" {
		fmt.Fprintf(sb, "**Archived:** true\n")
	}
	if node.Description != "" {
		fmt.Fprintf(sb, "\n%s\n", node.Description)
	}
	fmt.Fprintf(sb, "\nID: %s\n", node.Id)
}
