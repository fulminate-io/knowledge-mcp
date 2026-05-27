// SPDX-License-Identifier: Apache-2.0

package cloudwatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// cwPageLimit is the per-request event cap for FilterLogEvents. AWS documents
// 10,000 as the maximum (and default) events per API call — we use it purely
// as a BATCHING unit so pagination transfers large chunks at once. It is NOT
// a total ceiling on the collection; pagination continues via NextToken until
// either the cursor is exhausted or query.MaxEntries (when > 0) is reached.
const cwPageLimit = 10000

// Collect streams log entries from CloudWatch matching the query. It resolves
// the log group from query.Source or the provider's configured default,
// pages through FilterLogEvents results (up to 10K per page), normalizes
// each event to a LogEntry, and calls emit per page. query.MaxEntries caps
// the total number of entries returned when > 0; when <= 0, collection is
// unbounded from our side and runs until the API's NextToken is exhausted.
func (p *cloudwatchProvider) Collect(
	ctx context.Context,
	query logwire.Query,
	emit func(batch []logwire.LogEntry) error,
) error {
	client, err := p.ensureClient(ctx)
	if err != nil {
		return err
	}

	logGroupName := resolveLogGroup(query, p.logGroup)
	if logGroupName == "" {
		return fmt.Errorf("cloudwatch: no log group specified (set source or configure log_group)")
	}

	input := buildFilterInput(query, logGroupName)
	return paginate(ctx, client, input, query, emit)
}

// ListSources returns available CloudWatch log groups matching the prefix.
func (p *cloudwatchProvider) ListSources(ctx context.Context, prefix string) ([]logwire.Source, error) {
	client, err := p.ensureClient(ctx)
	if err != nil {
		return nil, err
	}

	input := &cloudwatchlogs.DescribeLogGroupsInput{Limit: aws.Int32(50)}
	if prefix != "" {
		input.LogGroupNamePrefix = aws.String(prefix)
	}

	var sources []logwire.Source
	for {
		out, err := client.DescribeLogGroups(ctx, input)
		if err != nil {
			if len(sources) > 0 {
				break //nolint:nilerr // return partial results already collected
			}
			return nil, fmt.Errorf("cloudwatch: DescribeLogGroups: %w", err)
		}
		for _, lg := range out.LogGroups {
			sources = append(sources, logwire.Source{
				Name:        aws.ToString(lg.LogGroupName),
				Provider:    "cloudwatch",
				Description: aws.ToString(lg.LogGroupName),
			})
			if len(sources) >= 100 {
				return sources, nil
			}
		}
		if out.NextToken == nil {
			break
		}
		input.NextToken = out.NextToken
	}
	return sources, nil
}

// resolveLogGroup picks the log group from the query source or provider
// default. A source containing "/" is treated as a full log group path.
func resolveLogGroup(q logwire.Query, defaultGroup string) string {
	if q.Source != "" {
		if strings.Contains(q.Source, "/") {
			return q.Source
		}
		if defaultGroup != "" {
			return defaultGroup + q.Source
		}
		return q.Source
	}
	return defaultGroup
}

// buildFilterInput constructs the FilterLogEventsInput from the query. The
// per-page Limit is always the CloudWatch per-request max (cwPageLimit);
// total-count enforcement (when q.MaxEntries > 0) lives in paginate().
func buildFilterInput(q logwire.Query, logGroup string) *cloudwatchlogs.FilterLogEventsInput {
	pageLimit := int32(cwPageLimit)
	if q.MaxEntries > 0 && int32(q.MaxEntries) < pageLimit {
		// If the caller asked for fewer than a full page total, shrink the
		// first request accordingly so we don't over-fetch.
		pageLimit = int32(q.MaxEntries)
	}

	input := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroup),
		Limit:        aws.Int32(pageLimit),
	}
	if !q.StartTime.IsZero() {
		input.StartTime = aws.Int64(q.StartTime.UnixMilli())
	}
	if !q.EndTime.IsZero() {
		input.EndTime = aws.Int64(q.EndTime.UnixMilli())
	}

	if q.RawQuery != "" {
		input.FilterPattern = aws.String(q.RawQuery)
	} else if pattern := buildFilterPattern(q); pattern != "" {
		input.FilterPattern = aws.String(pattern)
	}
	return input
}

// buildFilterPattern combines text filter and severity filter into a
// CloudWatch filter pattern string. CloudWatch patterns use simple syntax:
// quoted strings for exact match, JSON property matchers for structured logwire.
func buildFilterPattern(q logwire.Query) string {
	var parts []string
	if q.TextFilter != "" {
		parts = append(parts, fmt.Sprintf("%q", q.TextFilter))
	}
	// Note: SeverityMin is intentionally not used here — CloudWatch cannot
	// natively filter by severity level ordering. Severity filtering is done
	// client-side in paginate().
	return strings.Join(parts, " ")
}

// filterLogEventsClient captures the subset of cloudwatchlogs.Client that
// paginate uses, so tests can inject a fake. The concrete *cloudwatchlogs.Client
// satisfies it.
type filterLogEventsClient interface {
	FilterLogEvents(
		ctx context.Context,
		params *cloudwatchlogs.FilterLogEventsInput,
		optFns ...func(*cloudwatchlogs.Options),
	) (*cloudwatchlogs.FilterLogEventsOutput, error)
}

// paginate drives FilterLogEvents pagination, normalizes events, applies
// client-side filters, and calls emit per page. When query.MaxEntries <= 0
// the collection is unbounded and runs until the API's NextToken is empty.
// When query.MaxEntries > 0, collection stops once the total reaches the cap.
func paginate(
	ctx context.Context,
	client filterLogEventsClient,
	input *cloudwatchlogs.FilterLogEventsInput,
	query logwire.Query,
	emit func([]logwire.LogEntry) error,
) error {
	maxEntries := query.MaxEntries // 0 or negative means "unbounded"
	total := 0

	for {
		out, err := client.FilterLogEvents(ctx, input)
		if err != nil {
			if total > 0 {
				return nil //nolint:nilerr // partial results already emitted
			}
			return fmt.Errorf("cloudwatch: FilterLogEvents: %w", err)
		}

		batch := filterCloudWatchBatch(out.Events, aws.ToString(input.LogGroupName), query)
		if err := emitBatchRespectingCap(batch, maxEntries, total, emit); err != nil {
			return err
		}
		total += len(batch)
		if maxEntries > 0 && total >= maxEntries {
			break
		}
		if out.NextToken == nil {
			break
		}
		input.NextToken = out.NextToken
	}
	return nil
}

// filterCloudWatchBatch normalizes the page of FilterLogEvents results and
// applies client-side text + severity filters. Returning the filtered batch
// keeps paginate's cognitive complexity under the 30-complexity budget.
func filterCloudWatchBatch(
	events []cwtypes.FilteredLogEvent,
	logGroup string,
	query logwire.Query,
) []logwire.LogEntry {
	batch := make([]logwire.LogEntry, 0, len(events))
	for _, event := range events {
		entry := normalizeEntry(event, logGroup)
		if query.TextFilter != "" &&
			!strings.Contains(strings.ToLower(entry.Message), strings.ToLower(query.TextFilter)) {
			continue
		}
		if query.SeverityMin != "" && !logwire.SeverityAtLeast(entry.Severity, query.SeverityMin) {
			continue
		}
		batch = append(batch, entry)
	}
	return batch
}

// emitBatchRespectingCap emits the batch, truncating to the remaining cap
// when MaxEntries is bounded. Skips emit for empty batches so callers don't
// invoke emit with zero-length slices.
func emitBatchRespectingCap(
	batch []logwire.LogEntry,
	maxEntries, total int,
	emit func([]logwire.LogEntry) error,
) error {
	if len(batch) == 0 {
		return nil
	}
	if maxEntries > 0 {
		remaining := maxEntries - total
		if len(batch) > remaining {
			batch = batch[:remaining]
		}
	}
	return emit(batch)
}
