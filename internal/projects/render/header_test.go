// SPDX-License-Identifier: Apache-2.0

// Package render — header rendering tests for the backend-agnostic
// extension on tickets and projects.
//
// Co-located in the render package so the unexported
// renderTicketHeader / renderProjectHeader helpers stay accessible
// without exporting them solely for tests.
//
// These tests pin the rendered output directly (no store round-trip)
// so a future change to the section formatting can't quietly break
// the contract that:
//
//   - The adapter name (no "Linear", "Jira", ...) never appears in
//     either header — the deeplink is the only backend-identifying
//     surface.
//   - Tickets render External ID, URL, and (when set) Archived as
//     separate lines.
//   - Projects collapse External ID + URL onto a single
//     "External ID / URL:" line when they're equal — Linear-style
//     projects (where BuildProjectNode sets external_id = ref.URL).
//   - Projects fall back to a bare URL line when external_id is empty
//     but external_url is present.
//
// Generic placeholder values throughout. No real backend names, team
// keys, or workspace identifiers in the asserted strings.
//
// Verbatim port of cmd/knowledge-server/tools/tools_assemble_containers_render_test.go.
package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// renderTicketHeaderString returns the rendered header for a ticket
// node as a string — wraps the strings.Builder boilerplate.
func renderTicketHeaderString(n *knowledgev1.Node) string {
	var sb strings.Builder
	renderTicketHeader(n, &sb)
	return sb.String()
}

// renderProjectHeaderString returns the rendered header for a
// project node as a string.
func renderProjectHeaderString(n *knowledgev1.Node) string {
	var sb strings.Builder
	renderProjectHeader(n, &sb)
	return sb.String()
}

// TestRenderTicketHeader_BackendAgnostic: a ticket with backend
// metadata renders External ID, URL, and Status without naming the
// adapter. The `backend` metadata key carries the adapter name
// internally — render MUST NOT surface it.
func TestRenderTicketHeader_BackendAgnostic(t *testing.T) {
	n := &knowledgev1.Node{
		Id:         "ticket-1",
		Type:       string(kgtypes.NodeTicket),
		SymbolName: "test-ticket",
		Status:     kgtypes.StatusOpen,
	}
	kgtypes.SetValue(n, "backend", "lin_api_test")
	kgtypes.SetValue(n, "external_id", "ABC-42")
	kgtypes.SetValue(n, "external_url", "https://example.invalid/i/ABC-42")

	out := renderTicketHeaderString(n)
	assert.Contains(t, out, "**External ID:** ABC-42")
	assert.Contains(t, out, "**URL:** https://example.invalid/i/ABC-42")
	assert.NotContains(t, out, "Linear")
	assert.NotContains(t, out, "linear")
	assert.NotContains(t, out, "lin_api_test",
		"adapter name leaks into render — header must be backend-agnostic")
}

// TestRenderTicketHeader_Archived: a ticket with external_archived="true"
// renders an `**Archived:** true` line. Active tickets (the default)
// stay silent.
func TestRenderTicketHeader_Archived(t *testing.T) {
	n := &knowledgev1.Node{
		Id:         "ticket-arch",
		Type:       string(kgtypes.NodeTicket),
		SymbolName: "archived-ticket",
		Status:     "Done",
	}
	kgtypes.SetValue(n, "backend", "lin_api_test")
	kgtypes.SetValue(n, "external_id", "ABC-99")
	kgtypes.SetValue(n, "external_url", "https://example.invalid/i/ABC-99")
	kgtypes.SetValue(n, "external_archived", "true")

	out := renderTicketHeaderString(n)
	assert.Contains(t, out, "**Archived:** true")

	active := &knowledgev1.Node{
		Id:         "ticket-active",
		Type:       string(kgtypes.NodeTicket),
		SymbolName: "active-ticket",
	}
	kgtypes.SetValue(active, "external_archived", "false")
	activeOut := renderTicketHeaderString(active)
	assert.NotContains(t, activeOut, "Archived",
		"default-active ticket must not surface Archived line")
}

// TestRenderProjectHeader_BackendAgnostic: a project with
// external_id == external_url renders a single collapsed
// "External ID / URL:" line.
func TestRenderProjectHeader_BackendAgnostic(t *testing.T) {
	n := &knowledgev1.Node{
		Id:         "proj-1",
		Type:       string(kgtypes.NodeProject),
		SymbolName: "test-project",
		Status:     kgtypes.StatusActive,
	}
	const url = "https://example.invalid/p/proj_uuid_1"
	kgtypes.SetValue(n, "backend", "lin_api_test")
	kgtypes.SetValue(n, "external_id", url)
	kgtypes.SetValue(n, "external_url", url)

	out := renderProjectHeaderString(n)
	assert.Contains(t, out, "**External ID / URL:** "+url,
		"project external_id == external_url collapses to one line")
	assert.NotContains(t, out, "**External ID:** ",
		"non-collapsed External ID line leaked through — collapse path skipped")
	assert.NotContains(t, out, "**URL:** ",
		"non-collapsed URL line leaked through — collapse path skipped")
	assert.NotContains(t, out, "Linear")
	assert.NotContains(t, out, "linear")
	assert.NotContains(t, out, "lin_api_test")
}

// TestRenderProjectHeader_FallbackToURL_WhenExternalIDEmpty: when a
// project has external_url set but no external_id, render falls back
// to a bare URL line.
func TestRenderProjectHeader_FallbackToURL_WhenExternalIDEmpty(t *testing.T) {
	n := &knowledgev1.Node{
		Id:         "proj-2",
		Type:       string(kgtypes.NodeProject),
		SymbolName: "fallback-project",
		Status:     kgtypes.StatusActive,
	}
	const url = "https://example.invalid/p/proj_uuid_2"
	kgtypes.SetValue(n, "external_url", url)

	out := renderProjectHeaderString(n)
	assert.Contains(t, out, "**URL:** "+url,
		"empty external_id with present external_url falls back to bare URL line")
	assert.NotContains(t, out, "External ID / URL",
		"collapse path must not fire when external_id is empty")
	assert.NotContains(t, out, "**External ID:** ",
		"empty external_id must not surface a stub line")
}
