// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The four categories, declared here independently of the production set so the
// test states the taxonomy rather than restating the implementation. A new term
// has an obvious home among them.
var (
	wantAdmitting = []Operation{
		OpSearch, OpQuery, OpTraverse, OpTraverseGraphWide,
		OpFileSymbols, OpFileSymbolsSuffixFallback, OpAst, OpAstHydrate, OpAssemble,

		OpMutate, OpDelete, OpThoughts, OpRecordDecision,
		OpCreatePlan, OpCreateTicket, OpCreateProject, OpCreateResearch, OpCreateTestPlan,

		OpCollect, OpCollectChunk, OpCollectFinalize, OpCollectFetchSubgraph, OpCustomComputer,

		OpWorker,
	}

	// (A) Management operations — denied by the rule's own clause.
	wantManagement = []Operation{OpManage, OpSync}

	// (B) Self-admission vectors — background terms that would feed the set itself.
	wantSelfAdmission = []Operation{
		OpPipelineEmbedWriteback, OpInstructionBootstrap,
		OpSegmentReconcile, OpSegmentRepair, OpRebuildSegments, OpSegmentDeltaMerge,
		OpSegmentHeal, OpSegmentHorizonSeed,
		OpPipelineGapScan, OpPipelineGraphDiscovery, OpPipelineGenPoll,
		OpCorpusDeltaDrain, OpPropagationReflect, OpHiveMonitor,
	}

	// (C) No graph instance addressed.
	wantNoInstance = []Operation{OpHive, OpHelp, OpAnalyzeUsage, OpToolUnknown, OpUnstamped}
)

// TestWorkingSetAdmissionPartition_CoversEveryOperation is the declared-versus-
// consumed gate. A term declared in operation_vocab.go with no classification
// fails HERE rather than defaulting silently, which is what stops a future
// operation from quietly becoming an admission.
func TestWorkingSetAdmissionPartition_CoversEveryOperation(t *testing.T) {
	t.Parallel()

	categories := map[string][]Operation{
		"admitting":      wantAdmitting,
		"management":     wantManagement,
		"self-admission": wantSelfAdmission,
		"no-instance":    wantNoInstance,
	}

	// The four categories must be pairwise disjoint.
	seen := map[Operation]string{}
	for name, ops := range categories {
		for _, op := range ops {
			if prev, dup := seen[op]; dup {
				t.Fatalf("operation %q is classified in both %q and %q", op, prev, name)
			}
			seen[op] = name
		}
	}

	// Their union must be exactly AllOperations — no term unclassified, no term
	// classified that is not declared. Compared against the vocabulary itself
	// rather than against a count, so two terms swapped for each other cannot
	// balance out.
	declared := map[Operation]struct{}{}
	for _, op := range AllOperations {
		declared[op] = struct{}{}
		if _, ok := seen[op]; !ok {
			t.Errorf("operation %q is declared in operation_vocab.go but classified nowhere: "+
				"give it a home in one of the four categories", op)
		}
	}
	for op, name := range seen {
		if _, ok := declared[op]; !ok {
			t.Errorf("operation %q is classified under %q but is not in AllOperations", op, name)
		}
	}

	// AdmitsWorkingSet must agree term by term.
	for _, op := range wantAdmitting {
		assert.True(t, AdmitsWorkingSet(op), "operation %q must admit its target graph", op)
	}
	for name, ops := range categories {
		if name == "admitting" {
			continue
		}
		for _, op := range ops {
			assert.False(t, AdmitsWorkingSet(op),
				"operation %q is in the %q denying category and must NOT admit", op, name)
		}
	}
}

// TestManagementOperationsNeverAdmit pins the management bucket BY NAME. The
// rule's own clause is the only thing excluding these two: both address concrete
// graph instances, so the instance-key half of the gate does not stop them.
func TestManagementOperationsNeverAdmit(t *testing.T) {
	t.Parallel()

	const rule = `"manage operations do not count towards interaction"`

	assert.Equal(t, []Operation{OpManage, OpSync}, wantManagement,
		"the management category has exactly two members; sync is a management operation, "+
			"not a standalone special case")
	for _, op := range wantManagement {
		assert.False(t, AdmitsWorkingSet(op),
			"%s: %q must never admit. manage(status) fans a per-graph read across EVERY "+
				"account graph, so admitting it would admit the whole account in one call", rule, op)
	}

	// KNOWN-POSITIVE CONTROL: the same predicate does return true for a direct
	// user read, so the falses above are a classification and not a dead function.
	require.True(t, AdmitsWorkingSet(OpQuery), "control: a direct user read must admit")
}

// TestPipelineWritebackNeverAdmits pins the self-admission loophole the working
// set would otherwise have. With the recorder on the routed-call chokepoint that
// background and user traffic share, this classification is the SOLE mechanism
// keeping writeback out — there is no structural exclusion behind it.
func TestPipelineWritebackNeverAdmits(t *testing.T) {
	t.Parallel()

	assert.False(t, AdmitsWorkingSet(OpPipelineEmbedWriteback),
		"pipeline writeback is NOT an admission: only user interactions admit a graph, and "+
			"admitting the writeback would make the working set self-admitting and the whole "+
			"gate a no-op")

	// KNOWN-POSITIVE CONTROL, same predicate.
	require.True(t, AdmitsWorkingSet(OpMutate), "control: a user mutation must admit")
}
