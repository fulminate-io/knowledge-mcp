// SPDX-License-Identifier: Apache-2.0

package cloudwatch

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwTypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// fakeFilterClient replays a canned sequence of FilterLogEvents responses.
// Each element of pages represents the return value for one call; NextToken
// on each page drives the pagination loop (empty NextToken terminates it).
type fakeFilterClient struct {
	pages []cloudwatchlogs.FilterLogEventsOutput
	calls int
	err   error
}

func (f *fakeFilterClient) FilterLogEvents(
	_ context.Context,
	_ *cloudwatchlogs.FilterLogEventsInput,
	_ ...func(*cloudwatchlogs.Options),
) (*cloudwatchlogs.FilterLogEventsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	idx := f.calls
	f.calls++
	if idx >= len(f.pages) {
		// Exhausted pages — return empty with no NextToken so paginate stops.
		return &cloudwatchlogs.FilterLogEventsOutput{}, nil
	}
	out := f.pages[idx]
	return &out, nil
}

// makeEvents returns n synthetic FilteredLogEvents with sequential timestamps.
func makeEvents(n int, startTs int64) []cwTypes.FilteredLogEvent {
	events := make([]cwTypes.FilteredLogEvent, 0, n)
	for i := range n {
		ts := startTs + int64(i)
		msg := "entry-" + aws.ToString(aws.String(string(rune('a'+i%26))))
		events = append(events, cwTypes.FilteredLogEvent{
			Timestamp: &ts,
			Message:   &msg,
		})
	}
	return events
}

// TestBuildFilterInput_PerPageLimit verifies the per-request Limit is the
// CloudWatch page max when MaxEntries is unbounded, and is shrunk to the
// caller's total when MaxEntries < cwPageLimit.
func TestBuildFilterInput_PerPageLimit(t *testing.T) {
	tests := []struct {
		name       string
		maxEntries int
		wantLimit  int32
	}{
		{"unbounded uses full page limit", 0, cwPageLimit},
		{"negative treated as unbounded", -1, cwPageLimit},
		{"small cap shrinks first page", 250, 250},
		{"cap exactly at page limit", cwPageLimit, cwPageLimit},
		{"large cap still uses page limit", 25000, cwPageLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := logwire.Query{MaxEntries: tt.maxEntries}
			input := buildFilterInput(q, "/aws/test")
			if input.Limit == nil {
				t.Fatalf("Limit was nil")
			}
			if *input.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", *input.Limit, tt.wantLimit)
			}
		})
	}
}

// TestPaginate_UnboundedCollectsAll verifies that MaxEntries=0 drains all
// pages until NextToken is empty — explicitly covering more than the old
// 500 default to prove the cap is gone.
func TestPaginate_UnboundedCollectsAll(t *testing.T) {
	// Three pages of 400 events each = 1200 total — well above the old 500 cap.
	pages := []cloudwatchlogs.FilterLogEventsOutput{
		{Events: makeEvents(400, 1_700_000_000_000), NextToken: aws.String("p2")},
		{Events: makeEvents(400, 1_700_000_000_500), NextToken: aws.String("p3")},
		{Events: makeEvents(400, 1_700_000_001_000)}, // final page, no NextToken
	}
	client := &fakeFilterClient{pages: pages}

	var got []logwire.LogEntry
	emit := func(batch []logwire.LogEntry) error {
		got = append(got, batch...)
		return nil
	}

	input := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("/aws/test"),
	}
	err := paginate(context.Background(), client, input, logwire.Query{MaxEntries: 0}, emit)
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	if len(got) != 1200 {
		t.Errorf("collected %d entries, want 1200 (unbounded)", len(got))
	}
	if client.calls != 3 {
		t.Errorf("FilterLogEvents calls = %d, want 3", client.calls)
	}
}

// TestPaginate_BoundedStopsAtCap verifies MaxEntries > 0 still honors the
// explicit cap even when more pages are available.
func TestPaginate_BoundedStopsAtCap(t *testing.T) {
	pages := []cloudwatchlogs.FilterLogEventsOutput{
		{Events: makeEvents(400, 1_700_000_000_000), NextToken: aws.String("p2")},
		{Events: makeEvents(400, 1_700_000_000_500), NextToken: aws.String("p3")},
		{Events: makeEvents(400, 1_700_000_001_000)},
	}
	client := &fakeFilterClient{pages: pages}

	total := 0
	emit := func(batch []logwire.LogEntry) error {
		total += len(batch)
		return nil
	}

	input := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("/aws/test"),
	}
	err := paginate(context.Background(), client, input, logwire.Query{MaxEntries: 600}, emit)
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	if total != 600 {
		t.Errorf("collected %d entries, want 600 (capped)", total)
	}
	// Should have stopped after page 2 (400 from p1 + 200 trimmed from p2).
	if client.calls != 2 {
		t.Errorf("FilterLogEvents calls = %d, want 2", client.calls)
	}
}

// TestPaginate_UnboundedAboveOldHardCeiling verifies we can now collect more
// than the previous 10000 hard ceiling when MaxEntries is unbounded.
func TestPaginate_UnboundedAboveOldHardCeiling(t *testing.T) {
	// Two full pages (@cwPageLimit each) followed by a terminator page.
	pages := []cloudwatchlogs.FilterLogEventsOutput{
		{Events: makeEvents(cwPageLimit, 1_700_000_000_000), NextToken: aws.String("p2")},
		{Events: makeEvents(cwPageLimit, 1_700_000_001_000), NextToken: aws.String("p3")},
		{Events: makeEvents(500, 1_700_000_002_000)},
	}
	client := &fakeFilterClient{pages: pages}

	total := 0
	emit := func(batch []logwire.LogEntry) error {
		total += len(batch)
		return nil
	}

	input := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("/aws/test"),
	}
	err := paginate(context.Background(), client, input, logwire.Query{MaxEntries: 0}, emit)
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	wantTotal := cwPageLimit*2 + 500
	if total != wantTotal {
		t.Errorf("collected %d entries, want %d (previously capped at 10000)", total, wantTotal)
	}
}

// TestPaginate_FirstPageErrorReturnsError verifies hard failures on the first
// call still propagate (no partial results to salvage).
func TestPaginate_FirstPageErrorReturnsError(t *testing.T) {
	client := &fakeFilterClient{err: errors.New("throttled")}
	emit := func(batch []logwire.LogEntry) error { return nil }
	input := &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String("/aws/test")}
	err := paginate(context.Background(), client, input, logwire.Query{}, emit)
	if err == nil {
		t.Fatal("expected error propagation when first call fails")
	}
}
