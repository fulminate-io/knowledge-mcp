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
)

func TestInstanceGroupSubCollector_Name(t *testing.T) {
	c := &instanceGroupSubCollector{}
	assert.Equal(t, "gcp-instance-groups", c.Name())
}

// fakeInstanceGroupsPairIter mirrors fakeInstancesPairIter for the
// instance-groups AggregatedList iterator. See compute_collect_test.go
// for the design rationale.
type fakeInstanceGroupsPairIter struct {
	steps []instanceGroupsPairStep
	idx   int
}

type instanceGroupsPairStep struct {
	pair compute.InstanceGroupsScopedListPair
	err  error
}

func (f *fakeInstanceGroupsPairIter) Next() (compute.InstanceGroupsScopedListPair, error) {
	if f.idx >= len(f.steps) {
		return compute.InstanceGroupsScopedListPair{}, iterator.Done
	}
	s := f.steps[f.idx]
	f.idx++
	return s.pair, s.err
}

type fakeInstanceGroupsAggregator struct {
	iter *fakeInstanceGroupsPairIter
}

func (f fakeInstanceGroupsAggregator) AggregatedList(
	_ context.Context, _ string,
) instanceGroupsPairIter {
	return f.iter
}

func newInstanceGroupSubCollectorWithAggregator(
	agg instanceGroupsAggregator,
) *instanceGroupSubCollector {
	return &instanceGroupSubCollector{
		aggregator: agg,
		projectID:  testProjectID,
	}
}

// instanceGroupsPairForZone returns a successful pair carrying one minimal
// instance group. listMembers is NOT exercised here — the SDK client is
// nil and zoneName extraction would short-circuit anyway because the test
// instance group has no zone set in the URL form listMembers expects.
//
// To avoid invoking listMembers (which would dereference c.client), the
// fixture leaves Zone empty. lastSegment("") returns "" so listMembers
// returns nil immediately. The SelfLink is still present so the resource
// is emitted.
func instanceGroupsPairForZone(zone string) compute.InstanceGroupsScopedListPair {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/" +
		zone + "/instanceGroups/ig-" + zone
	return compute.InstanceGroupsScopedListPair{
		Key: "zones/" + zone,
		Value: &computepb.InstanceGroupsScopedList{
			InstanceGroups: []*computepb.InstanceGroup{{
				Name:     new("ig-" + zone),
				SelfLink: new(selfLink),
				// Zone left empty so listMembers returns nil without
				// needing the SDK client. See doc comment above.
			}},
		},
	}
}

func TestInstanceGroupSubCollector_PerZone403_Continues(t *testing.T) {
	apiErr := &googleapi.Error{Code: http.StatusForbidden, Message: "permission denied"}
	iter := &fakeInstanceGroupsPairIter{steps: []instanceGroupsPairStep{
		{pair: instanceGroupsPairForZone("us-central1-a")},
		{err: apiErr},
	}}
	c := newInstanceGroupSubCollectorWithAggregator(
		fakeInstanceGroupsAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, res.Resources, 1)
	assert.Equal(t, "ig-us-central1-a", res.Resources[0].Name)
}

func TestInstanceGroupSubCollector_ProjectLevel403_ReturnsEmpty(t *testing.T) {
	apiErr := &googleapi.Error{Code: http.StatusForbidden, Message: "permission denied"}
	iter := &fakeInstanceGroupsPairIter{steps: []instanceGroupsPairStep{
		{err: apiErr},
	}}
	c := newInstanceGroupSubCollectorWithAggregator(
		fakeInstanceGroupsAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, res.Resources)
}

func TestInstanceGroupSubCollector_OtherError_Propagates(t *testing.T) {
	// instancegroup.go wraps non-permission errors with fmt.Errorf("%w").
	// errors.Is must still unwrap the original sentinel.
	netErr := errors.New("network: connection reset")
	iter := &fakeInstanceGroupsPairIter{steps: []instanceGroupsPairStep{
		{err: netErr},
	}}
	c := newInstanceGroupSubCollectorWithAggregator(
		fakeInstanceGroupsAggregator{iter: iter})

	_, err := c.Collect(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, netErr)
}

func TestInstanceGroupSubCollector_UnreachableWarning_SkipsZone(t *testing.T) {
	warnPair := compute.InstanceGroupsScopedListPair{
		Key: "zones/us-east1-b",
		Value: &computepb.InstanceGroupsScopedList{
			Warning: &computepb.Warning{
				Code:    new(computepb.Warning_UNREACHABLE.String()),
				Message: new("Scope unreachable"),
			},
		},
	}
	iter := &fakeInstanceGroupsPairIter{steps: []instanceGroupsPairStep{
		{pair: warnPair},
		{pair: instanceGroupsPairForZone("us-central1-a")},
	}}
	c := newInstanceGroupSubCollectorWithAggregator(
		fakeInstanceGroupsAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, res.Resources, 1)
	assert.Equal(t, "ig-us-central1-a", res.Resources[0].Name)
}
