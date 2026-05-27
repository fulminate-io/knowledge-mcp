// SPDX-License-Identifier: Apache-2.0

// Package loki implements a log Provider for Grafana Loki using the HTTP API.
// It queries logs via LogQL and discovers sources via the label values endpoint.
package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func init() {
	logwire.Register("loki", func() logwire.Provider { return &lokiProvider{} })
}

// lokiProvider implements logwire.Provider for Grafana Loki.
type lokiProvider struct {
	address     string
	tenantID    string
	username    string
	password    string
	bearerToken string
	httpClient  *http.Client
}

// Configure reads Loki connection settings from the config map.
// Required key: "url" (or "address"). Optional: "tenant_id", "username",
// "password", "bearer_token".
func (p *lokiProvider) Configure(config map[string]string) error {
	p.address = config["url"]
	if p.address == "" {
		p.address = config["address"]
	}
	if p.address == "" {
		return fmt.Errorf("loki: url or address is required")
	}
	p.address = strings.TrimRight(p.address, "/")

	p.tenantID = config["tenant_id"]
	p.username = config["username"]
	p.password = config["password"]
	p.bearerToken = config["bearer_token"]
	p.httpClient = &http.Client{Timeout: 30 * time.Second}
	return nil
}

// lokiPageLimit is the per-request cap for /loki/api/v1/query_range. Loki's
// default server-side limit is 5000 entries per query; we use the same value
// as a BATCHING unit so each page transfers a large chunk at once. It is NOT
// a total ceiling: when query.MaxEntries <= 0 we paginate by narrowing the
// time window (see paginateByTimeNarrowing) until the API returns fewer
// entries than the page limit.
const lokiPageLimit = 5000

// Collect queries Loki for log entries matching the query and streams them
// via the emit callback. Pagination: Loki's query_range has no offset/cursor,
// so we fetch a page of up to lokiPageLimit entries with direction=backward
// and, if more entries may exist, re-query with end = oldest_returned_nanos
// to walk backwards until the window is exhausted or query.MaxEntries is
// reached (when > 0).
func (p *lokiProvider) Collect(ctx context.Context, query logwire.Query, emit func([]logwire.LogEntry) error) error {
	logQL := buildLogQL(query)
	maxEntries := query.MaxEntries // 0 or negative means "unbounded"

	startNanos, endNanos := lokiTimeRangeNanos(query)
	return p.paginateByTimeNarrowing(ctx, logQL, query, startNanos, endNanos, maxEntries, emit)
}

// lokiTimeRangeNanos resolves the query's time range into (start, end)
// nanosecond pairs. endNanos==0 means "no end bound" — Loki treats missing
// end as "now". startNanos==0 means "no start bound".
func lokiTimeRangeNanos(query logwire.Query) (int64, int64) {
	var start, end int64
	if !query.StartTime.IsZero() {
		start = query.StartTime.UnixNano()
	}
	if !query.EndTime.IsZero() {
		end = query.EndTime.UnixNano()
	}
	return start, end
}

// paginateByTimeNarrowing pages through Loki by repeatedly shrinking the
// end timestamp to the oldest entry returned. direction=backward means each
// page returns the NEWEST entries in the window; to get older pages we set
// end = oldest_returned - 1ns and query again. The loop terminates when a
// page returns fewer than lokiPageLimit entries, when maxEntries is reached
// (if > 0), or when the window collapses (end <= start).
func (p *lokiProvider) paginateByTimeNarrowing(
	ctx context.Context,
	logQL string,
	query logwire.Query,
	startNanos, endNanos int64,
	maxEntries int,
	emit func([]logwire.LogEntry) error,
) error {
	total := 0
	pageLimit := lokiPageLimit
	if maxEntries > 0 && maxEntries < pageLimit {
		pageLimit = maxEntries
	}

	for {
		// fetchPage returns both the post-filter slice (for emit) AND the
		// raw page tail timestamp + raw count (for termination + window
		// narrowing). Using the filtered count for either was a silent
		// truncation bug: a severity-filtered query against a high-volume
		// stream would terminate after one page because 47 ERROR entries
		// (post-filter) was < 5000 (pageLimit), even though the un-queried
		// remainder of the time window held thousands more errors.
		page, err := p.fetchPage(ctx, logQL, query, startNanos, endNanos, pageLimit)
		if err != nil {
			if total > 0 {
				return nil //nolint:nilerr // partial results already emitted
			}
			return err
		}
		if page.rawCount == 0 {
			return nil
		}

		emitted, err := emitPageRespectingCap(page.entries, maxEntries, total, emit)
		if err != nil {
			return err
		}
		total += emitted

		if maxEntries > 0 && total >= maxEntries {
			return nil
		}
		// If the RAW page was not full, Loki has no more entries in [start,
		// end]. Filtered count can't tell us this — only the API's view of
		// the underlying stream can.
		if page.rawCount < pageLimit {
			return nil
		}

		// Narrow the window: next page's end is one nanosecond before the
		// raw oldest entry we received. Using the filtered oldest would skip
		// any older raw entries that happened to pass the filter.
		nextEnd := page.rawOldestNanos - 1
		if startNanos > 0 && nextEnd <= startNanos {
			return nil
		}
		if endNanos > 0 && nextEnd >= endNanos {
			// Defensive: should not happen when pageLimit was reached, but
			// guarantees loop progress.
			return nil
		}
		endNanos = nextEnd
	}
}

// emitPageRespectingCap invokes emit on entries truncated to fit under
// maxEntries-total (when maxEntries > 0). Returns the count actually emitted
// so the caller can update its running total. Empty pages emit nothing.
func emitPageRespectingCap(
	entries []logwire.LogEntry, maxEntries, total int,
	emit func([]logwire.LogEntry) error,
) (int, error) {
	if maxEntries > 0 {
		remaining := maxEntries - total
		if len(entries) > remaining {
			entries = entries[:remaining]
		}
	}
	if len(entries) == 0 {
		return 0, nil
	}
	if err := emit(entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}

// lokiPageResult carries both the post-filter slice the caller wants to emit
// AND the raw-page metadata (count + oldest timestamp) the caller needs to
// terminate / narrow correctly. Splitting them out is the whole point of the
// fix for finding dd2b4dce.
type lokiPageResult struct {
	entries        []logwire.LogEntry
	rawCount       int
	rawOldestNanos int64
}

// fetchPage issues a single query_range request and returns the normalized
// sort-by-time-desc entries for that page along with the raw count + raw
// oldest timestamp so the caller can terminate and narrow the window
// without confusing the post-filter view with the underlying stream's.
func (p *lokiProvider) fetchPage(
	ctx context.Context,
	logQL string,
	query logwire.Query,
	startNanos, endNanos int64,
	pageLimit int,
) (lokiPageResult, error) {
	params := url.Values{
		"query":     {logQL},
		"limit":     {strconv.Itoa(pageLimit)},
		"direction": {"backward"},
	}
	if startNanos > 0 {
		params.Set("start", strconv.FormatInt(startNanos, 10))
	}
	if endNanos > 0 {
		params.Set("end", strconv.FormatInt(endNanos, 10))
	}

	body, err := p.doRequest(ctx, http.MethodGet, "/loki/api/v1/query_range", params)
	if err != nil {
		return lokiPageResult{}, fmt.Errorf("loki: query_range: %w", err)
	}

	var resp lokiQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return lokiPageResult{}, fmt.Errorf("loki: parse response: %w", err)
	}
	if resp.Status != "success" {
		return lokiPageResult{}, fmt.Errorf("loki: query failed: status=%s", resp.Status)
	}

	rawEntries, filteredEntries := normalizeResultsRaw(resp.Data.Result, query)
	sort.Slice(filteredEntries, func(i, j int) bool {
		return filteredEntries[i].Timestamp.After(filteredEntries[j].Timestamp)
	})

	out := lokiPageResult{
		entries:  filteredEntries,
		rawCount: len(rawEntries),
	}
	if len(rawEntries) > 0 {
		// Raw oldest = newest-last after sorting raw entries, which match
		// the API's chronological order. Iterate to find min timestamp
		// without a separate sort (raw count can be 5000+).
		oldest := rawEntries[0].Timestamp.UnixNano()
		for _, e := range rawEntries[1:] {
			if t := e.Timestamp.UnixNano(); t < oldest {
				oldest = t
			}
		}
		out.rawOldestNanos = oldest
	}
	return out, nil
}

// normalizeResultsRaw converts Loki streams into LogEntry slices, returning
// both the raw entries (pre-filter) and the post-filter slice. Pagination
// uses the raw count + raw oldest timestamp to avoid silent truncation when
// client-side filters drop a page below the API's pageLimit.
func normalizeResultsRaw(streams []lokiStream, query logwire.Query) (raw, filtered []logwire.LogEntry) {
	for _, stream := range streams {
		for _, val := range stream.Values {
			if len(val) < 2 {
				continue
			}
			entry := normalizeEntry(stream.Stream, val[0], val[1])
			raw = append(raw, entry)
			if query.TextFilter != "" &&
				!strings.Contains(strings.ToLower(entry.Message), strings.ToLower(query.TextFilter)) {
				continue
			}
			if query.SeverityMin != "" && !logwire.SeverityAtLeast(entry.Severity, query.SeverityMin) {
				continue
			}
			filtered = append(filtered, entry)
		}
	}
	return raw, filtered
}

// ListSources discovers available namespaces (or jobs) from Loki label values.
func (p *lokiProvider) ListSources(ctx context.Context, prefix string) ([]logwire.Source, error) {
	values, err := p.listLabelValues(ctx, "namespace")
	if err != nil || len(values) == 0 {
		values, err = p.listLabelValues(ctx, "job")
		if err != nil {
			return nil, fmt.Errorf("loki: list sources: %w", err)
		}
	}

	var sources []logwire.Source
	for _, val := range values {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(val), strings.ToLower(prefix)) {
			continue
		}
		sources = append(sources, logwire.Source{
			Name:        val,
			Provider:    "loki",
			Description: val,
		})
		if len(sources) >= 100 {
			break
		}
	}
	return sources, nil
}

// listLabelValues queries Loki for values of a specific label, scoped to the
// last 24 hours for relevance.
func (p *lokiProvider) listLabelValues(ctx context.Context, label string) ([]string, error) {
	now := time.Now()
	params := url.Values{
		"start": {strconv.FormatInt(now.Add(-24*time.Hour).UnixNano(), 10)},
		"end":   {strconv.FormatInt(now.UnixNano(), 10)},
	}

	path := "/loki/api/v1/label/" + url.PathEscape(label) + "/values"
	body, err := p.doRequest(ctx, http.MethodGet, path, params)
	if err != nil {
		return nil, err
	}

	var resp lokiLabelResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("loki: parse label response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("loki: label query failed: status=%s", resp.Status)
	}
	return resp.Data, nil
}

// doRequest executes an HTTP request against the Loki instance with auth headers.
func (p *lokiProvider) doRequest(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	u := p.address + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("loki: build request: %w", err)
	}

	if p.username != "" {
		req.SetBasicAuth(p.username, p.password)
	} else if p.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.bearerToken)
	}
	if p.tenantID != "" {
		req.Header.Set("X-Scope-OrgID", p.tenantID)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("loki: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody := string(body)
		if len(errBody) > 200 {
			errBody = errBody[:200] + "..."
		}
		return nil, fmt.Errorf("loki: HTTP %d: %s", resp.StatusCode, errBody)
	}
	return body, nil
}

// Loki API response types.

type lokiQueryResponse struct {
	Status string        `json:"status"`
	Data   lokiQueryData `json:"data"`
}

type lokiQueryData struct {
	ResultType string       `json:"resultType"`
	Result     []lokiStream `json:"result"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"` // Each value is [nanosecond_string, line].
}

type lokiLabelResponse struct {
	Status string   `json:"status"`
	Data   []string `json:"data"`
}
