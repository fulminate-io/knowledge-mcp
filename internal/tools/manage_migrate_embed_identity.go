// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	kgtools "github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// manage_migrate_embed_identity.go is the operator-facing surface of the ONLY
// operation that changes a graph's already-recorded embed identity.
//
// WHY IT IS A MANAGE OPERATION AND NOT A CLI SUBCOMMAND: settled by ruling, and
// the mechanical reason is that the CLI shape had no transport. The identity
// field on the scan and writeback are READS; the write needs its own operator on
// the Execute envelope, which is what MUTATION_KIND_SET_EMBED_IDENTITY is.
//
// A PROFILE NAME, NOT AN INLINE IDENTITY. An operator names a profile the config
// already defines; the identity is resolved from it here. Accepting four loose
// fields would let a migration name an embedder no profile describes, and then
// the client that has to construct it for a query has nowhere to find its
// credential.

// handleMigrateEmbedIdentity resolves the named profile and drives the
// server-side migration through the existing Execute envelope.
//
// THE SPEND IS ANNOUNCED, which is the half of "explicit spend" that is about
// the OPERATOR rather than the architecture. The decision's cost-safety spine
// has two halves: only this operation can trigger a corpus-scale re-embed, and
// the operator sees what it will cost. The result reports the identity
// transition and the number of vectors cleared — which is exactly the number of
// nodes the pipeline is about to re-embed, and therefore the bill.
func handleMigrateEmbedIdentity(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	if a.Graph == "" {
		return errorResult("migrate_embed_identity: graph is required — the operation migrates ONE named graph")
	}
	if a.Name == "" {
		return errorResult("migrate_embed_identity: name is required — the operation migrates ONE named graph")
	}
	if a.Profile == "" {
		return errorResult(
			"migrate_embed_identity: profile is required — name the embedder profile to migrate TO " +
				"(a profile the config defines, or \"default\" for the single [embedder] table)")
	}

	// AN UNKNOWN PROFILE IS REFUSED NAMING THE DEFINED ONES, never defaulted. A
	// migration that quietly used the default profile would record an identity
	// the operator did not choose — and because the record is authoritative
	// afterwards, that wrong choice is permanent short of another migration.
	if !config.Loaded() {
		return errorResult("migrate_embed_identity: no config is loaded, so no profile can be resolved")
	}
	prof, err := config.Active().EmbedProfileByName(a.Profile)
	if err != nil {
		return errorResult("migrate_embed_identity: " + err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("migrate_embed_identity: graph client unavailable")
	}

	// THE IDENTITY BEING LEFT IS READ BEFORE THE WRITE, from the SERVER'S RECORD
	// via the graph catalog — never from this machine's config, which describes
	// what this machine would embed with rather than what the graph was embedded
	// under. Reading it first is what lets the result state a TRANSITION rather
	// than just a destination: an operator who cannot see what they are leaving
	// cannot tell a no-op migration from a corpus-scale one.
	//
	// A FAILED READ IS AN ERROR, and it stops the migration BEFORE any spend.
	// Proceeding with an unknown "from" would announce half a transition on the
	// one operation whose whole point is that its cost is explicit.
	previous, err := recordedIdentityFor(ctx, gc, a.Graph, a.Name)
	if err != nil {
		return errorResult("migrate_embed_identity: " + err.Error())
	}

	req := &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: a.Graph, Name: a.Name, Repo: a.Name},
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_SET_EMBED_IDENTITY,
			SetEmbedIdentity: &knowledgev1.SetEmbedIdentity{
				GraphType: a.Graph,
				GraphName: a.Name,
				Identity: &knowledgev1.EmbedIdentity{
					Provider:  prof.Provider.String(),
					Model:     prof.Model,
					Dimension: int32(prof.Dimension), //nolint:gosec // a width from the accepted set, max 2048
					Dtype:     prof.Dtype,
				},
			},
		}},
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return errorResult("migrate_embed_identity: " + err.Error())
	}

	to := fmt.Sprintf("%s/%s at %d %s", prof.Provider, prof.Model, prof.Dimension, prof.Dtype)
	out := map[string]any{
		"graph":   a.Graph,
		"name":    a.Name,
		"profile": prof.Name,
		// THE TRANSITION, both ends. "identity" keeps naming the destination for
		// anything already reading that key.
		"identity":            to,
		"identity_from":       renderIdentity(previous),
		"identity_to":         to,
		"identity_transition": fmt.Sprintf("%s -> %s", renderIdentity(previous), to),
		"vectors_cleared":     resp.GetAffectedCount(),
		// The count IS the cost, said in words as well as a number so an operator
		// reading the result does not have to infer what it implies.
		"note": fmt.Sprintf(
			"%d node(s) will be re-embedded by the pipeline at the new identity; that is the "+
				"embedding spend this migration just committed to",
			resp.GetAffectedCount()),
	}
	body, mErr := json.Marshal(out)
	if mErr != nil {
		return errorResult("migrate_embed_identity: render result: " + mErr.Error())
	}
	return textResult(string(body))
}

// recordedIdentityFor reads ONE graph's recorded embed identity from the server's
// catalog. A nil return means the graph records none — nothing has been embedded
// there yet, which is a legitimate starting state and not an error.
func recordedIdentityFor(
	ctx context.Context, gc GraphCaller, graphType, name string,
) (*knowledgev1.EmbedIdentity, error) {
	infos, err := fetchGraphNamesOfType(ctx, gc, graphType)
	if err != nil {
		return nil, fmt.Errorf("read the %s catalog to learn the identity being superseded: %w", graphType, err)
	}
	return identityForGraph(infos, name), nil
}

// renderIdentity spells one identity for an operator, and says so plainly when
// there is none rather than rendering an empty tuple that reads like a bug.
func renderIdentity(id *knowledgev1.EmbedIdentity) string {
	if id == nil || id.GetProvider() == "" {
		return "none recorded (nothing has been embedded in this graph yet)"
	}
	return fmt.Sprintf("%s/%s at %d %s",
		id.GetProvider(), id.GetModel(), id.GetDimension(), id.GetDtype())
}
