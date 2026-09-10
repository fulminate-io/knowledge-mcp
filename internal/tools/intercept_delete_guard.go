// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_delete_guard.go carries the `delete` tool's param-accounting guard.
//
// WHY A GUARD INTERCEPT RATHER THAN AN INSERTION. Every other tool's params are
// decoded somewhere in this package, so its accounting call has a natural home
// at the decode site's tool-name gate. delete has none: its arguments are
// decoded client-side but in the sibling package engine (the compile path, the
// dry-run preview, and the completed-delete renderer). engine cannot call the
// accounting helper because tools imports engine, so the reverse import is a
// cycle; forking the helper into engine would leave two accounting mechanisms
// where the whole point is to have one. Hence a minimal intercept in THIS
// package that claims the tool name, accounts, and otherwise gets out of the way.
//
// THE FALL-THROUGH RETURN IS THE WHOLE CONTRACT. A delete carrying nothing to
// reject returns NOT-handled, so the call proceeds UNCHANGED to the dispatcher,
// which claims the dry-run preview and compiles the real delete. A guard that
// returned handled on a clean payload would swallow every delete silently —
// which is why the test drives a clean payload and asserts fall-through, not
// just that a dirty one is refused.
//
// ClientDeps is unused: the check reads only the payload and the published
// schema. The two-blank-parameter signature mirrors InterceptHelp.
func InterceptDeleteGuard(_ context.Context, _ ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "delete" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("delete", "", DeleteToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	// REQUIREMENT 4 ON THE TENTH WRITE ARM. `delete` is the one write the mutate
	// interceptors never see, so the refusal every mutate arm carries has to be
	// stated again here or the field is accepted, dropped by the compiler, and
	// the delete runs against the combined graph while the caller believed it had
	// scoped itself to practice/go — on a DESTRUCTIVE op, which is the worst
	// place in the tool surface for a silent redirect. The schema already
	// documents the refusal; this is what performs it.
	//
	// The selector is decoded locally rather than through the engine's delete
	// args: engine imports nothing from this package and this package imports
	// engine, so reaching for its decoder is the cycle this file's header
	// records. Two fields is a smaller duplication than a second accounting
	// mechanism, and an unparseable payload is left to the compiler to report.
	var sel struct {
		Graph    string `json:"graph"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(params.Arguments, &sel); err == nil {
		if err := refusePracticeLanguageOnWrite(sel.Graph, sel.Language, practiceHubParamFree); err != nil {
			return true, errorResult("delete: " + err.Error())
		}
	}
	return false, kgtools.ToolResult{}
}
