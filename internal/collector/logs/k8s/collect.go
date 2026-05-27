// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	eventsv1client "k8s.io/client-go/kubernetes/typed/events/v1"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// emitBatchSize is the number of entries the provider buffers before
// invoking the caller's emit callback. Chosen to match the stackdriver
// provider so pipeline batch accounting is consistent across backends.
const emitBatchSize = 500

// Collect lists Kubernetes Events via the events.k8s.io/v1 API, paginates
// through Continue tokens, normalizes each Event to one or two LogEntry
// values (series events contribute two), and emits matching entries in
// bounded batches. Filters are split between server-side ListOptions
// (SeverityMin >= WARN + canonical FieldFilters) and client-side
// predicates (text match, severity safety-net, time range, un-pushed-down
// FieldFilters). query.MaxEntries <= 0 means "no cap — collect everything
// matching within the time range". Otherwise the cap counts emitted
// LogEntry values (not Events iterated), so a two-entry series counts as 2.
//
// Events v1 field notes (different from core/v1):
//   - primary timestamp: e.EventTime (metav1.MicroTime)
//   - series aggregation: e.Series.Count / e.Series.LastObservedTime
//   - subject object ref: e.Regarding (was InvolvedObject in core/v1)
//   - message text: e.Note (was Message in core/v1)
//   - back-compat fallbacks: e.DeprecatedFirstTimestamp,
//     e.DeprecatedLastTimestamp, e.DeprecatedCount (used by normalizeEvent
//     when EventTime / Series.LastObservedTime are unset on shimmed events).
func (p *k8sProvider) Collect(
	ctx context.Context,
	query logwire.Query,
	emit func(batch []logwire.LogEntry) error,
) error {
	cs, err := p.ensureClientset()
	if err != nil {
		return err
	}

	return drainEvents(ctx, cs.EventsV1().Events(""), query, p.kubeContext, p.contextForLogging(ctx), emit)
}

// drainEvents is the pagination loop. It owns the Continue-token loop,
// graceful degradation when the apiserver rejects the FieldSelector,
// client-side filtering, batched emission, and the MaxEntries short-circuit.
//
// Extracted from Collect so the main method stays under the 80-line budget
// and so tests can inject an events.k8s.io/v1 lister directly.
func drainEvents(
	ctx context.Context,
	lister eventsv1client.EventInterface,
	query logwire.Query,
	kubeContext string,
	contextForLogging string,
	emit func([]logwire.LogEntry) error,
) error {
	opts := buildListOptions(query)
	batch := make([]logwire.LogEntry, 0, emitBatchSize)
	emitted := 0
	serverFilterOK := true

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := emit(batch); err != nil {
			return err
		}
		emitted += len(batch)
		batch = batch[:0]
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		list, err := listOnePage(ctx, lister, opts, &serverFilterOK)
		if err != nil {
			return fmt.Errorf("k8s logs: list events (context=%s): %w", contextForLogging, err)
		}

		done, ferr := appendFilteredBatch(list.Items, kubeContext, query, &batch, &emitted, flush)
		if ferr != nil {
			return ferr
		}
		if done || list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}
	return flush()
}

// listOnePage performs a single List call with graceful degradation. If the
// apiserver rejects the field selector (older versions / custom allowlists),
// we clear FieldSelector and retry once — subsequent pages then use the
// degraded opts for the rest of the call. The flag is captured via pointer
// so the caller can persist the degraded state across pages.
func listOnePage(
	ctx context.Context,
	lister eventsv1client.EventInterface,
	opts metav1.ListOptions,
	serverFilterOK *bool,
) (*eventsv1.EventList, error) {
	if !*serverFilterOK {
		opts.FieldSelector = ""
	}
	list, err := lister.List(ctx, opts)
	if err == nil {
		return list, nil
	}
	if *serverFilterOK && opts.FieldSelector != "" && isFieldSelectorRejection(err) {
		// One-shot retry without the server-side selector. Subsequent pages
		// skip the selector too (captured via the pointer).
		*serverFilterOK = false
		opts.FieldSelector = ""
		return lister.List(ctx, opts)
	}
	return nil, err
}

// appendFilteredBatch runs each Event through normalizeEvent, applies the
// client-side filter, and flushes through emit in emitBatchSize batches.
// Returns done=true when query.MaxEntries is reached, signaling drainEvents
// to stop paginating. A MaxEntries <= 0 value means "no cap".
func appendFilteredBatch(
	events []eventsv1.Event,
	kubeContext string,
	query logwire.Query,
	batch *[]logwire.LogEntry,
	emitted *int,
	flush func() error,
) (bool, error) {
	maxEntries := query.MaxEntries
	for i := range events {
		for _, entry := range normalizeEvent(&events[i], kubeContext) {
			if !passesClientFilter(entry, query) {
				continue
			}
			if maxEntries > 0 && *emitted+len(*batch) >= maxEntries {
				return true, flush()
			}
			*batch = append(*batch, entry)
			if len(*batch) >= emitBatchSize {
				if err := flush(); err != nil {
					return true, err
				}
			}
		}
	}
	return false, nil
}

// ListSources returns a single Source identifying the configured
// kubecontext. One Source per context matches the "logical log backend"
// granularity used elsewhere in the package — cluster-wide Event listing
// has no finer-grained source concept to surface. The prefix filter is
// honored so the tools layer can search-as-you-type against the context
// name.
func (p *k8sProvider) ListSources(ctx context.Context, prefix string) ([]logwire.Source, error) {
	_ = ctx // reserved for future per-context API probing
	name := p.kubeContext
	if name == "" {
		name = "default"
	}

	if prefix != "" && !stringsHasPrefixFold(name, prefix) {
		return []logwire.Source{}, nil
	}

	return []logwire.Source{{
		Name:        name,
		Provider:    "k8s",
		Description: "Kubernetes Events (events.k8s.io/v1) for context " + name,
	}}, nil
}

// stringsHasPrefixFold is a tiny case-insensitive HasPrefix to avoid
// pulling strings in solely for that one-liner. Kept local so the k8s
// package stays importable without strings side-effects.
func stringsHasPrefixFold(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if toLowerASCII(s[i]) != toLowerASCII(prefix[i]) {
			return false
		}
	}
	return true
}

func toLowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
