// SPDX-License-Identifier: Apache-2.0

// compile_mutate_target.go holds the mutation Target builder: the projection of
// a caller's selector fields onto the ONE field the target family consumes.
//
// It is split from compile_mutate.go because it answers a different question
// from the rest of that file. compile_mutate.go lowers a mutate payload into a
// MutationPlan — WHAT to write; this decides WHERE it goes, and it is the seam
// two production defects came through, so it is worth finding on its own.

package engine

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mutateTarget builds a mutation's Target carrying ONLY the instance field the
// target family actually consumes, and nothing else.
//
// THE PROJECTION IS THE WHOLE POINT. A selector field the target family does not
// consume is REFUSED by the server, not ignored — so handing it every field the
// caller happened to supply is not permissive, it is a guaranteed rejection the
// moment any of them is set. Two production failures came from exactly that:
// the mutate tool's `name` param is the NODE name and rode into the selector,
// and then `language` did the same one field over.
//
// DERIVED, NEVER DUPLICATED. Which field a family consumes is answered by
// graphsel.InstanceField and nowhere else; graphsel.InstanceValueOf reads the
// caller's value for that field. This replaced a hand-maintained map of
// name-blind families, which is the shape that produced both defects: a second
// copy of one partition, updated in one place and not the others. There is no
// language-blind twin to add, because there is no per-field list at all.
//
// ONE RULE, EVERY ARM. All four Target-building sites route through this helper:
// mutationRequest (create/upsert/by-id update/link/unlink),
// compileMutateUpdateBatch and compileMutateBulkMetadata
// (compile_mutate_batch.go), and deleteRequest (compile_delete.go).
//
// transformersBucketName is the one Target name that is a LITERAL rather than
// caller input, so it is applied after the projection.
//
// A nil Target is preserved for the all-empty case, which is how the knowledge
// default is addressed; buildTarget's own callers rely on the same convention.
func mutateTarget(graph, repo, account, name, language, branch string) *knowledgev1.GraphSelector {
	gt := kgtypes.GraphType(graph)
	instance := graphsel.InstanceValueOf(gt, repo, account, name, language)
	if graph == transformersGraphFamily {
		instance = transformersBucketName
	}
	if graph == "" && instance == "" && branch == "" {
		return nil
	}
	sel := graphsel.GraphSelectorFor(gt, instance, false)
	// Branch is consumed by the code family alone, alongside its repo.
	if branch != "" && gt == kgtypes.GraphCode {
		sel.Branch = branch
	}
	return sel
}

// transformersGraphFamily is the wire graph string whose Target name is pinned
// to the canonical bucket rather than taken from the caller.
const transformersGraphFamily = "transformers"
