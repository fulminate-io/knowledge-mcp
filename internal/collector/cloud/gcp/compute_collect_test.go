// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"errors"
	"net/http"
	"testing"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeInstancesPairIter drives computeSubCollector.Collect with a scripted
// sequence of (pair, err) pairs returned from successive Next() calls. The
// final entry should typically be (zero-pair, iterator.Done) to terminate
// the loop normally.
type fakeInstancesPairIter struct {
	steps []instancesPairStep
	idx   int
}

type instancesPairStep struct {
	pair compute.InstancesScopedListPair
	err  error
}

func (f *fakeInstancesPairIter) Next() (compute.InstancesScopedListPair, error) {
	if f.idx >= len(f.steps) {
		return compute.InstancesScopedListPair{}, iterator.Done
	}
	s := f.steps[f.idx]
	f.idx++
	return s.pair, s.err
}

// fakeInstancesAggregator returns a pre-built fake iterator on the single
// AggregatedList call Collect makes.
type fakeInstancesAggregator struct {
	iter *fakeInstancesPairIter
}

func (f fakeInstancesAggregator) AggregatedList(
	_ context.Context, _ string,
) instancesPairIter {
	return f.iter
}

// testProjectID is the synthetic project name used across the
// fake-aggregator tests. The aggregator implementations don't inspect it,
// but ResourceSpec content paths do — keeping it shared makes assertions
// readable and matches what the fixture builders embed in selfLinks.
const testProjectID = "p"

// newComputeSubCollectorWithAggregator builds a subcollector wired to a
// test aggregator, leaving the SDK client nil. Only the iterator-driven
// Collect path uses this; nothing else on the struct needs the client.
func newComputeSubCollectorWithAggregator(
	agg instancesAggregator,
) *computeSubCollector {
	return &computeSubCollector{
		aggregator: agg,
		projectID:  testProjectID,
	}
}

// instancesPairForZone is a fixture builder: a successful pair carrying one
// minimally-populated instance whose selfLink is unique per zone.
func instancesPairForZone(zone string) compute.InstancesScopedListPair {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/" +
		zone + "/instances/vm-" + zone
	return compute.InstancesScopedListPair{
		Key: "zones/" + zone,
		Value: &computepb.InstancesScopedList{
			Instances: []*computepb.Instance{{
				Name:     new("vm-" + zone),
				SelfLink: new(selfLink),
				Zone:     new("projects/p/zones/" + zone),
			}},
		},
	}
}

func TestComputeSubCollector_PerZone403_Continues(t *testing.T) {
	// First a good zone yields one instance, then a per-zone permission
	// error mid-iteration. The collector must preserve what it has and
	// return nil error so the parent collector keeps running.
	apiErr := &googleapi.Error{Code: http.StatusForbidden, Message: "permission denied"}
	iter := &fakeInstancesPairIter{steps: []instancesPairStep{
		{pair: instancesPairForZone("us-central1-a")},
		{err: apiErr},
	}}
	c := newComputeSubCollectorWithAggregator(
		fakeInstancesAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, res.Resources, 1)
	assert.Equal(t,
		"https://www.googleapis.com/compute/v1/projects/p/zones/us-central1-a/instances/vm-us-central1-a",
		res.Resources[0].ID)
}

func TestComputeSubCollector_ProjectLevel403_ReturnsEmpty(t *testing.T) {
	// First Next() call returns 403 — no zones yielded yet, so this is
	// project-level. Result must be empty + nil so parent collector
	// continues with other subcollectors.
	apiErr := &googleapi.Error{Code: http.StatusForbidden, Message: "permission denied"}
	iter := &fakeInstancesPairIter{steps: []instancesPairStep{
		{err: apiErr},
	}}
	c := newComputeSubCollectorWithAggregator(
		fakeInstancesAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, res.Resources)
	assert.Empty(t, res.Edges)
}

func TestComputeSubCollector_ProjectLevel403_Grpc(t *testing.T) {
	// Same as above but the deny surfaces as a grpc status (defensive
	// coverage for non-REST clients on similar paths).
	grpcErr := status.Error(codes.PermissionDenied, "permission denied")
	iter := &fakeInstancesPairIter{steps: []instancesPairStep{
		{err: grpcErr},
	}}
	c := newComputeSubCollectorWithAggregator(
		fakeInstancesAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, res.Resources)
}

func TestComputeSubCollector_UnreachableWarning_SkipsZone(t *testing.T) {
	// A pair carrying a UNREACHABLE warning (the partial-success signal)
	// must be skipped without contributing instances; iteration continues
	// into the next pair.
	warnPair := compute.InstancesScopedListPair{
		Key: "zones/us-east1-b",
		Value: &computepb.InstancesScopedList{
			Warning: &computepb.Warning{
				Code:    new(computepb.Warning_UNREACHABLE.String()),
				Message: new("Scope unreachable"),
			},
		},
	}
	iter := &fakeInstancesPairIter{steps: []instancesPairStep{
		{pair: warnPair},
		{pair: instancesPairForZone("us-central1-a")},
	}}
	c := newComputeSubCollectorWithAggregator(
		fakeInstancesAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, res.Resources, 1)
	assert.Equal(t, "vm-us-central1-a", res.Resources[0].Name)
}

func TestComputeSubCollector_OtherError_Propagates(t *testing.T) {
	// A non-permission error (e.g. transient network failure) is NOT
	// swallowed — it propagates to the caller. Tested with a bare error
	// value (no googleapi/grpc shape).
	netErr := errors.New("network: connection reset")
	iter := &fakeInstancesPairIter{steps: []instancesPairStep{
		{err: netErr},
	}}
	c := newComputeSubCollectorWithAggregator(
		fakeInstancesAggregator{iter: iter})

	_, err := c.Collect(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, netErr)
}
