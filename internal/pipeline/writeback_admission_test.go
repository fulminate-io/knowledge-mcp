// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// writeback_admission_test.go proves the per-domain writeback gate ACTS: that it
// bounds in-flight writebacks per lock domain, that its key matches the domain
// the server actually locks, and that a rate-limited writeback parked in its
// backoff does not hold the domain shut.
//
// EVERY TEST USES A UNIQUE GRAPH NAME. The permit store is package-level (the
// domains belong to the server, not to a Pipeline instance), so tests sharing a
// name would share permits and one test's parked writer would be another's
// flake.

// admissionDomain names the domain a request targeted, recovered from the first
// update item's id prefix. Reading it off the payload rather than the Target
// selector keeps the fake independent of which selector field a family routes
// its instance key through.
func admissionDomain(req *knowledgev1.ExecuteRequest) string {
	items := req.GetMutation().GetUpdateItems()
	if len(items) == 0 {
		return ""
	}
	name, _, _ := strings.Cut(items[0].GetId(), "/")
	return name
}

// barrierClient parks every Execute until release is closed, announcing each
// arrival first. Parking is what makes concurrency OBSERVABLE: a fake that
// returned immediately would let every writer through one at a time and read
// the same peak of 1 whether the gate worked or was absent entirely.
type barrierClient struct {
	mu       sync.Mutex
	inFlight map[string]int
	peaks    map[string]int
	arrivals chan string
	release  chan struct{}
}

func newBarrierClient() *barrierClient {
	return &barrierClient{
		inFlight: map[string]int{},
		peaks:    map[string]int{},
		arrivals: make(chan string, 64),
		release:  make(chan struct{}),
	}
}

func (b *barrierClient) PipelineGenPoll(context.Context, *knowledgev1.PipelineGenPollRequest) (*knowledgev1.PipelineGenPollResponse, error) {
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (b *barrierClient) PipelineScan(context.Context, *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	return &knowledgev1.PipelineScanResponse{}, nil
}

func (b *barrierClient) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	domain := admissionDomain(req)
	b.mu.Lock()
	b.inFlight[domain]++
	if b.inFlight[domain] > b.peaks[domain] {
		b.peaks[domain] = b.inFlight[domain]
	}
	b.mu.Unlock()
	b.arrivals <- domain
	<-b.release
	b.mu.Lock()
	b.inFlight[domain]--
	b.mu.Unlock()
	return &knowledgev1.ExecuteResponse{}, nil
}

func (b *barrierClient) peak(domain string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peaks[domain]
}

// admissionItems builds one writeback payload whose ids carry the domain, so the
// fake can attribute each Execute without inspecting the selector.
func admissionItems(domain string, n int) []updateBatchItem {
	items := make([]updateBatchItem, 0, n)
	for i := range n {
		items = append(items, updateBatchItem{
			ID:       fmt.Sprintf("%s/item%d", domain, i),
			Metadata: map[string]string{"embed_failure_reason": ""},
		})
	}
	return items
}

// TestWritebackAdmission_PerGraphWidth asserts the gate bounds one domain at
// WritebackHoldersPerGraph while a DIFFERENT domain still proceeds.
//
// The second half is not decoration: a globally-shared gate passes the first
// assertion perfectly and is precisely wrong, because it would make an idle
// graph's writeback wait behind a busy graph's queue.
func TestWritebackAdmission_PerGraphWidth(t *testing.T) {
	ctx := context.Background()
	bc := newBarrierClient()
	const domainA, domainB = "admwidth-a", "admwidth-b"

	var wg sync.WaitGroup
	writers := WritebackHoldersPerGraph + 1
	for range writers {
		wg.Go(func() {
			_ = writeBatchUpdates(ctx, bc, kgtypes.GraphKnowledge, domainA, admissionItems(domainA, 1))
		})
	}

	for range WritebackHoldersPerGraph {
		require.Equal(t, domainA, waitArrival(t, bc, 5*time.Second), "the first %d writers must be admitted", WritebackHoldersPerGraph)
	}
	requireNoArrival(t, bc, 300*time.Millisecond, "writer %d on one domain must wait: the gate's width is %d", writers, WritebackHoldersPerGraph)

	// A different lock domain is unaffected — the property a global gate breaks.
	wg.Go(func() {
		_ = writeBatchUpdates(ctx, bc, kgtypes.GraphKnowledge, domainB, admissionItems(domainB, 1))
	})
	require.Equal(t, domainB, waitArrival(t, bc, 5*time.Second), "a writeback to a DIFFERENT lock domain must not queue behind a saturated one")

	close(bc.release)
	wg.Wait()
	require.Equal(t, WritebackHoldersPerGraph, bc.peak(domainA), "peak concurrent in-flight Execute on one domain")
	require.Equal(t, 1, bc.peak(domainB))
}

func waitArrival(t *testing.T, bc *barrierClient, within time.Duration) string {
	t.Helper()
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case d := <-bc.arrivals:
		return d
	case <-timer.C:
		t.Fatalf("no Execute arrived within %s", within)
		return ""
	}
}

func requireNoArrival(t *testing.T, bc *barrierClient, within time.Duration, msg string, args ...any) {
	t.Helper()
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case d := <-bc.arrivals:
		t.Fatalf("unexpected Execute arrival from %q: "+msg, append([]any{d}, args...)...)
	case <-timer.C:
	}
}

// TestAdmissionKey_MatchesServerLockDomain pins the key rule against the domain
// the SERVER locks, which is the whole correctness content of the key.
//
// A code branch overlay's graph key carries its "@branch" suffix all the way to
// the advisory key, so base and overlay take different locks and must not share
// a permit budget. Every other family's instance key is the pre-"@" base by the
// time it reaches the server, so its overlay-tagged writebacks all land on the
// base graph's lock and MUST share one.
func TestAdmissionKey_MatchesServerLockDomain(t *testing.T) {
	codeBase := admissionKeyFor(kgtypes.GraphCode, "repo")
	codeOverlay := admissionKeyFor(kgtypes.GraphCode, "repo@branch")
	require.Equal(t, "repo@branch", codeOverlay.GraphName, "the code family keeps its overlay suffix: repo#write and repo@branch#write are different advisory keys")
	require.NotEqual(t, codeBase, codeOverlay, "a code overlay and its base must not share one permit budget — they share no server lock")

	knowledgeBase := admissionKeyFor(kgtypes.GraphKnowledge, "default")
	knowledgeOverlay := admissionKeyFor(kgtypes.GraphKnowledge, "default@session-x")
	require.Equal(t, "default", knowledgeOverlay.GraphName, "a non-code family's instance key reaches the server as the pre-@ base, so its writeback locks the base graph")
	require.Equal(t, knowledgeBase, knowledgeOverlay, "two writebacks the server serializes on ONE lock must share ONE permit budget")

	require.NotEqual(t, admissionKeyFor(kgtypes.GraphCode, "x"), admissionKeyFor(kgtypes.GraphKnowledge, "x"), "the graph TYPE is part of the server's key")
}

// rateLimitClient 429s the first Execute for each parked id with a long
// Retry-After, and serves everything else immediately.
type rateLimitClient struct {
	mu       sync.Mutex
	seen     map[string]int
	markers  int
	arrivals chan string
	hint     time.Duration
}

func (r *rateLimitClient) PipelineGenPoll(context.Context, *knowledgev1.PipelineGenPollRequest) (*knowledgev1.PipelineGenPollResponse, error) {
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (r *rateLimitClient) PipelineScan(context.Context, *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	return &knowledgev1.PipelineScanResponse{}, nil
}

func (r *rateLimitClient) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	id := req.GetMutation().GetUpdateItems()[0].GetId()
	r.mu.Lock()
	r.seen[id]++
	first := r.seen[id] == 1
	parked := strings.Contains(id, "parked")
	if !parked {
		r.markers++
	}
	r.mu.Unlock()
	r.arrivals <- id
	if parked && first {
		return nil, &llm.LLMError{Transient: true, Reason: "http_429", RetryAfter: r.hint}
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestWritebackAdmission_RateLimitDoesNotBlockMarkerWrite is the liveness
// property, and it is the reason the permit is taken around ONE Execute rather
// than around the retry loop.
//
// The retry honors the server's Retry-After hint with NO CAP. If the permit
// spanned the loop, WritebackHoldersPerGraph rate-limited writebacks would hold
// every permit for the domain for as long as the backend asked — and the writes
// they would block include the terminal failure markers that are the only thing
// breaking the eligibility loop. So: saturate the domain's width with parked
// writebacks, then require an unrelated marker write to the SAME domain to land
// promptly.
func TestWritebackAdmission_RateLimitDoesNotBlockMarkerWrite(t *testing.T) {
	const domain = "admratelimit"
	const hint = 30 * time.Second
	rc := &rateLimitClient{seen: map[string]int{}, arrivals: make(chan string, 64), hint: hint}

	parkCtx, cancelPark := context.WithCancel(context.Background())
	defer cancelPark()
	var wg sync.WaitGroup
	for i := range WritebackHoldersPerGraph {
		wg.Go(func() {
			_ = writeBatchUpdates(parkCtx, rc, kgtypes.GraphKnowledge, domain,
				[]updateBatchItem{{ID: fmt.Sprintf("%s/parked%d", domain, i), Metadata: map[string]string{"k": "v"}}})
		})
	}
	// Every parked writer has made its rate-limited round trip and is now asleep
	// in the backoff. Under the correct placement it holds no permit here.
	for range WritebackHoldersPerGraph {
		select {
		case <-rc.arrivals:
		case <-time.After(5 * time.Second):
			t.Fatal("parked writebacks never reached Execute")
		}
	}

	start := time.Now()
	err := writeBatchUpdates(context.Background(), rc, kgtypes.GraphKnowledge, domain,
		[]updateBatchItem{{ID: domain + "/marker0", Metadata: map[string]string{"embed_failure_reason": "terminal"}}})
	elapsed := time.Since(start)
	require.NoError(t, err)
	t.Logf("marker write completed in %s while %d writebacks were parked on a %s Retry-After hint", elapsed, WritebackHoldersPerGraph, hint)
	require.Less(t, elapsed, 5*time.Second, "a marker write to a domain whose writebacks are parked in rate-limit backoff must not wait for their hint (%s)", hint)
	// The known positive: the marker write did not finish fast by doing nothing.
	rc.mu.Lock()
	markers := rc.markers
	rc.mu.Unlock()
	require.Equal(t, 1, markers, "the marker write must actually have reached the wire")

	cancelPark()
	wg.Wait()
}
