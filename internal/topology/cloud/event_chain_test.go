// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// event_chain_test.go covers EventChainAnalyzer: linear chains, fan-out,
// cycle detection, empty graph, non-cloud graph, and TopK capping. The
// graph is served over the wire by the cloud fixture's fake Execute.

const ecAccount = "event-chain-test"

func runEventChain(t *testing.T, fx *cloudFixture, topK int) []foundation.Finding {
	t.Helper()
	findings, err := EventChainAnalyzer{}.Run(context.Background(), fx.cloudReq(ecAccount, topK))
	require.NoError(t, err)
	return findings
}

func TestEventChainAnalyzer_Name(t *testing.T) {
	assert.Equal(t, "event_chain", EventChainAnalyzer{}.Name())
}

// TestEventChainAnalyzer_LinearChain builds:
//
//	EventBridge rule -> Lambda -> SQS -> Lambda
//
// Expect chain_length=3, severity=Notice.
func TestEventChainAnalyzer_LinearChain(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(ecAccount, "eb-rule-1", "eb-rule-1", "eventbridge-rule", nil)
	fx.AddCloudResource(ecAccount, "lambda-1", "lambda-1", "lambda-function", nil)
	fx.AddCloudResource(ecAccount, "sqs-1", "sqs-1", "sqs-queue", nil)
	fx.AddCloudResource(ecAccount, "lambda-2", "lambda-2", "lambda-function", nil)

	fx.AddEdge(ecAccount, "eb-rule-1", "lambda-1", kgtypes.EdgeTargets)
	fx.AddEdge(ecAccount, "lambda-1", "sqs-1", kgtypes.EdgeTargets)
	fx.AddEdge(ecAccount, "sqs-1", "lambda-2", kgtypes.EdgeTriggers)

	findings := runEventChain(t, fx, 0)
	require.NotEmpty(t, findings)

	var chainFinding *foundation.Finding
	for i := range findings {
		if findings[i].Title != "" && findings[i].Metrics["chain_length"] == 3 {
			chainFinding = &findings[i]
			break
		}
	}
	require.NotNil(t, chainFinding, "expected a chain finding with chain_length=3")
	assert.Equal(t, "event_chain", chainFinding.Algorithm)
	assert.Equal(t, foundation.SeverityNotice, chainFinding.Severity)
	assert.InDelta(t, 3, chainFinding.Metrics["chain_length"], 0.01)
	assert.Contains(t, chainFinding.Evidence, "eb-rule-1")
}

// TestEventChainAnalyzer_FanOut builds:
//
//	EventBridge rule -> Lambda1, Lambda2, Lambda3
//
// Expect fan_out=3.
func TestEventChainAnalyzer_FanOut(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(ecAccount, "eb-fan", "eb-fan", "eventbridge-rule", nil)
	fx.AddCloudResource(ecAccount, "fn-a", "fn-a", "lambda-function", nil)
	fx.AddCloudResource(ecAccount, "fn-b", "fn-b", "lambda-function", nil)
	fx.AddCloudResource(ecAccount, "fn-c", "fn-c", "lambda-function", nil)

	fx.AddEdge(ecAccount, "eb-fan", "fn-a", kgtypes.EdgeTargets)
	fx.AddEdge(ecAccount, "eb-fan", "fn-b", kgtypes.EdgeTargets)
	fx.AddEdge(ecAccount, "eb-fan", "fn-c", kgtypes.EdgeTargets)

	findings := runEventChain(t, fx, 0)
	require.NotEmpty(t, findings)

	var found bool
	for _, f := range findings {
		if f.Metrics["fan_out"] == 3 {
			found = true
			assert.Equal(t, "event_chain", f.Algorithm)
			assert.Contains(t, f.Evidence, "eb-fan")
		}
	}
	assert.True(t, found, "expected a finding with fan_out=3")
}

// TestEventChainAnalyzer_CircularLoop builds:
//
//	SNS -> SQS -> Lambda -> SNS (cycle)
//
// Expect a circular loop finding with severity=Warning.
func TestEventChainAnalyzer_CircularLoop(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(ecAccount, "sns-loop", "sns-loop", "sns-topic", nil)
	fx.AddCloudResource(ecAccount, "sqs-loop", "sqs-loop", "sqs-queue", nil)
	fx.AddCloudResource(ecAccount, "fn-loop", "fn-loop", "lambda-function", nil)

	fx.AddEdge(ecAccount, "sns-loop", "sqs-loop", kgtypes.EdgeSubscribesTo)
	fx.AddEdge(ecAccount, "sqs-loop", "fn-loop", kgtypes.EdgeTriggers)
	fx.AddEdge(ecAccount, "fn-loop", "sns-loop", kgtypes.EdgeTargets)

	findings := runEventChain(t, fx, 0)
	require.NotEmpty(t, findings)

	var cycleFinding *foundation.Finding
	for i := range findings {
		if findings[i].Severity == foundation.SeverityWarning &&
			strings.Contains(findings[i].Title, "Circular") {
			cycleFinding = &findings[i]
			break
		}
	}
	require.NotNil(t, cycleFinding, "expected a circular loop finding")
	assert.Equal(t, foundation.SeverityWarning, cycleFinding.Severity)
	assert.Contains(t, cycleFinding.Evidence, "sns-loop")
}

// TestEventChainAnalyzer_EmptyGraph verifies no event sources means nil.
func TestEventChainAnalyzer_EmptyGraph(t *testing.T) {
	fx := newCloudFixture(t)
	fx.account(ecAccount)
	findings := runEventChain(t, fx, 0)
	assert.Nil(t, findings)
}

// TestEventChainAnalyzer_NonCloudGraph verifies GraphKnowledge returns error.
func TestEventChainAnalyzer_NonCloudGraph(t *testing.T) {
	fx := newCloudFixture(t)
	req := foundation.Request{Caller: fx, Graph: kgtypes.GraphKnowledge, Name: "default"}
	_, err := EventChainAnalyzer{}.Run(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires GraphCloud")
}

// TestEventChainAnalyzer_TopK verifies the TopK cap.
func TestEventChainAnalyzer_TopK(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 3 {
		src := fmt.Sprintf("eb-topk-%d", i)
		tgt := fmt.Sprintf("fn-topk-%d", i)
		fx.AddCloudResource(ecAccount, src, src, "eventbridge-rule", nil)
		fx.AddCloudResource(ecAccount, tgt, tgt, "lambda-function", nil)
		fx.AddEdge(ecAccount, src, tgt, kgtypes.EdgeTargets)
	}

	findings := runEventChain(t, fx, 1)
	assert.Len(t, findings, 1)
}
