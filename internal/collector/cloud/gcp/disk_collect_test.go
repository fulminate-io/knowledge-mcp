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
	"google.golang.org/protobuf/proto"
)

// fakeDisksPairIter mirrors fakeInstancesPairIter for the disk
// AggregatedList iterator surface. See compute_collect_test.go for the
// design rationale.
type fakeDisksPairIter struct {
	steps []disksPairStep
	idx   int
}

type disksPairStep struct {
	pair compute.DisksScopedListPair
	err  error
}

func (f *fakeDisksPairIter) Next() (compute.DisksScopedListPair, error) {
	if f.idx >= len(f.steps) {
		return compute.DisksScopedListPair{}, iterator.Done
	}
	s := f.steps[f.idx]
	f.idx++
	return s.pair, s.err
}

type fakeDisksAggregator struct {
	iter *fakeDisksPairIter
}

func (f fakeDisksAggregator) AggregatedList(
	_ context.Context, _ string,
) disksPairIter {
	return f.iter
}

func newDiskSubCollectorWithAggregator(
	agg disksAggregator,
) *diskSubCollector {
	return &diskSubCollector{
		aggregator: agg,
		projectID:  testProjectID,
	}
}

func disksPairForZone(zone string) compute.DisksScopedListPair {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/" +
		zone + "/disks/d-" + zone
	return compute.DisksScopedListPair{
		Key: "zones/" + zone,
		Value: &computepb.DisksScopedList{
			Disks: []*computepb.Disk{{
				Name:     new("d-" + zone),
				SelfLink: new(selfLink),
				Zone:     new("projects/p/zones/" + zone),
				SizeGb:   proto.Int64(10),
			}},
		},
	}
}

func TestDiskSubCollector_PerZone403_Continues(t *testing.T) {
	apiErr := &googleapi.Error{Code: http.StatusForbidden, Message: "permission denied"}
	iter := &fakeDisksPairIter{steps: []disksPairStep{
		{pair: disksPairForZone("us-central1-a")},
		{err: apiErr},
	}}
	c := newDiskSubCollectorWithAggregator(
		fakeDisksAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, res.Resources, 1)
	assert.Equal(t, "d-us-central1-a", res.Resources[0].Name)
}

func TestDiskSubCollector_ProjectLevel403_ReturnsEmpty(t *testing.T) {
	apiErr := &googleapi.Error{Code: http.StatusForbidden, Message: "permission denied"}
	iter := &fakeDisksPairIter{steps: []disksPairStep{
		{err: apiErr},
	}}
	c := newDiskSubCollectorWithAggregator(
		fakeDisksAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, res.Resources)
}

func TestDiskSubCollector_OtherError_Propagates(t *testing.T) {
	netErr := errors.New("network: connection reset")
	iter := &fakeDisksPairIter{steps: []disksPairStep{
		{err: netErr},
	}}
	c := newDiskSubCollectorWithAggregator(
		fakeDisksAggregator{iter: iter})

	_, err := c.Collect(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, netErr)
}

func TestDiskSubCollector_UnreachableWarning_SkipsZone(t *testing.T) {
	warnPair := compute.DisksScopedListPair{
		Key: "zones/us-east1-b",
		Value: &computepb.DisksScopedList{
			Warning: &computepb.Warning{
				Code:    new(computepb.Warning_UNREACHABLE.String()),
				Message: new("Scope unreachable"),
			},
		},
	}
	iter := &fakeDisksPairIter{steps: []disksPairStep{
		{pair: warnPair},
		{pair: disksPairForZone("us-central1-a")},
	}}
	c := newDiskSubCollectorWithAggregator(
		fakeDisksAggregator{iter: iter})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, res.Resources, 1)
	assert.Equal(t, "d-us-central1-a", res.Resources[0].Name)
}
