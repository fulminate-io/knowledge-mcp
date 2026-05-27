// SPDX-License-Identifier: Apache-2.0

package stackdriver

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/logging"
	"cloud.google.com/go/logging/logadmin"
	"google.golang.org/api/iterator"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// emitBatchSize is the number of normalized entries the provider buffers
// before calling the emit callback. GCP's logadmin iterator yields entries
// one at a time (page size is a hint, not a hard cap on the server), so we
// flush to the caller every 500 entries to keep memory bounded and give the
// pipeline something to work with before Collect returns.
const emitBatchSize = 500

// Collect streams log entries from GCP Cloud Logging matching the query.
// It builds an Advanced Logs Filter, iterates the logadmin Entries result,
// normalizes each entry via normalizeEntry, and emits in batches of up to
// emitBatchSize. query.MaxEntries caps the total number of entries returned
// when > 0; zero or negative values mean "unbounded" — collection runs until
// the iterator reports iterator.Done. Memory is bounded by the streaming
// emitBatchSize flush, not by a total cap.
func (p *stackdriverProvider) Collect(
	ctx context.Context,
	query logwire.Query,
	emit func(batch []logwire.LogEntry) error,
) error {
	client, err := p.ensureClient(ctx)
	if err != nil {
		return err
	}

	maxEntries := query.MaxEntries // 0 or negative means "unbounded"

	filter := buildStackdriverFilter(p.projectID, query)
	opts := []logadmin.EntriesOption{
		logadmin.Filter(filter),
		logadmin.NewestFirst(),
	}
	it := client.Entries(ctx, opts...)

	return drainEntries(&logadminNexter{it: it}, p.projectID, query, maxEntries, emit)
}

// entryNexter is the minimal surface drainEntries needs from an entries
// iterator. Tests substitute an in-memory implementation; production wraps
// *logadmin.EntryIterator via logadminNexter.
type entryNexter interface {
	Next() (*logging.Entry, error)
}

// logadminNexter adapts *logadmin.EntryIterator to entryNexter.
type logadminNexter struct {
	it *logadmin.EntryIterator
}

func (l *logadminNexter) Next() (*logging.Entry, error) {
	return l.it.Next()
}

// drainEntries walks the entries iterator, applies client-side text
// filtering, and flushes emit in batches. When maxEntries > 0 it stops once
// the total reaches the cap; when maxEntries <= 0 it drains to iterator.Done.
// Extracted from Collect so the main method stays well under the 80-line
// function budget.
func drainEntries(
	it entryNexter,
	projectID string,
	query logwire.Query,
	maxEntries int,
	emit func([]logwire.LogEntry) error,
) error {
	batch := make([]logwire.LogEntry, 0, emitBatchSize)
	total := 0

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := emit(batch); err != nil {
			return err
		}
		total += len(batch)
		batch = batch[:0]
		return nil
	}

	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			if total > 0 || len(batch) > 0 {
				_ = flush() // best-effort partial emit
				return nil  //nolint:nilerr // partial results already delivered
			}
			return fmt.Errorf("stackdriver: read entries: %w", err)
		}

		le := normalizeEntry(entry, projectID)
		if !passesClientFilter(le, query) {
			continue
		}
		batch = append(batch, le)

		if len(batch) >= emitBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		if maxEntries > 0 && total+len(batch) >= maxEntries {
			break
		}
	}

	// Trim the last batch if a bounded cap would be exceeded.
	if maxEntries > 0 {
		if remaining := maxEntries - total; remaining < len(batch) {
			batch = batch[:remaining]
		}
	}
	return flush()
}

// passesClientFilter applies text and severity predicates that GCP cannot
// express (or that we apply defensively after the server-side filter). The
// severity path is a safety net: the filter already includes
// "severity >= X", but the reclassification in normalizeEntry may downgrade
// an entry below the threshold, and we never want to emit something the
// caller asked us to exclude.
func passesClientFilter(le logwire.LogEntry, query logwire.Query) bool {
	if query.TextFilter != "" &&
		!strings.Contains(strings.ToLower(le.Message), strings.ToLower(query.TextFilter)) {
		return false
	}
	if query.SeverityMin != "" && !logwire.SeverityAtLeast(le.Severity, query.SeverityMin) {
		return false
	}
	return true
}

// ListSources returns available log names for the configured GCP project,
// converting the full "projects/<project>/logs/<id>" paths to short IDs for
// display. Results are capped at 100 to keep responses bounded.
func (p *stackdriverProvider) ListSources(ctx context.Context, prefix string) ([]logwire.Source, error) {
	client, err := p.ensureClient(ctx)
	if err != nil {
		return nil, err
	}

	it := client.Logs(ctx)
	seen := make(map[string]bool)
	var sources []logwire.Source
	for {
		logName, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			if len(sources) > 0 {
				break // return partial results
			}
			return nil, fmt.Errorf("stackdriver: list logs: %w", err)
		}

		shortName := extractLogID(logName)
		if prefix != "" && !strings.HasPrefix(strings.ToLower(shortName), strings.ToLower(prefix)) {
			continue
		}
		if seen[shortName] {
			continue
		}
		seen[shortName] = true

		sources = append(sources, logwire.Source{
			Name:        shortName,
			Provider:    "stackdriver",
			Description: shortName,
		})
		if len(sources) >= 100 {
			break
		}
	}
	return sources, nil
}
