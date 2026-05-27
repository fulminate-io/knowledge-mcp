// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// fakeDependencyChecker is a deterministic stub the correlation tests use
// to exercise both the "confirmed" and "unconfirmed" paths without pulling
// a real cloud graph into the logs package. It returns true iff the
// (a.ID, b.ID) pair matches a pre-seeded entry. Account fields are
// ignored because the tests construct ResolvedResources with a single
// shared account.
type fakeDependencyChecker struct {
	known map[string]struct{}
	calls int
}

func newFakeChecker(pairs ...[2]string) *fakeDependencyChecker {
	m := make(map[string]struct{}, len(pairs))
	for _, p := range pairs {
		m[p[0]+"|"+p[1]] = struct{}{}
		m[p[1]+"|"+p[0]] = struct{}{}
	}
	return &fakeDependencyChecker{known: m}
}

func (f *fakeDependencyChecker) HasDependency(_ context.Context, a, b ResolvedResource) bool {
	f.calls++
	_, ok := f.known[a.ID+"|"+b.ID]
	return ok
}

// resolverFromMap returns a CloudResolver that resolves service-name
// labels via the supplied service→ID map. All hits share the
// "test-acct" account so newFakeChecker pairs (which key on ID only)
// continue to work after the ResolvedResource refactor.
func resolverFromMap(services map[string]string) CloudResolver {
	return &mockResolver{services: services, account: "test-acct"}
}

// templateAt is a test helper producing a synthetic error template whose
// FirstSeen/LastSeen cover [start, start+dur] with a stable ID derived
// from its pattern.
func templateAt(id, pattern string, start time.Time, dur time.Duration, severity string) *wirelogs.LogTemplate {
	return &wirelogs.LogTemplate{
		ID:        id,
		Pattern:   pattern,
		Severity:  severity,
		Count:     10,
		FirstSeen: start,
		LastSeen:  start.Add(dur),
	}
}

// streamFor constructs a stream whose labels point at svc. The ID matches
// the fingerprint so chunkFor can wire template↔stream correctly.
func streamFor(svc string) *wirelogs.LogStream {
	labels := map[string]string{wirelogs.FieldService: svc}
	return &wirelogs.LogStream{
		ID:             "stream-" + svc,
		Labels:         labels,
		LowCardLabels:  labels,
		HighCardLabels: map[string]string{},
		Fingerprint:    "stream-" + svc + "-fp",
	}
}

// chunkFor is a minimal chunk whose StreamID/TemplateID establish the
// linkage correlation logic needs; timestamps cover [t, t+dur].
func chunkFor(stream *wirelogs.LogStream, tmpl *wirelogs.LogTemplate, t time.Time, dur time.Duration) *wirelogs.LogChunk {
	return &wirelogs.LogChunk{
		ID:         "chunk-" + tmpl.ID + "-" + stream.ID,
		StreamID:   stream.ID,
		TemplateID: tmpl.ID,
		StartTime:  t,
		EndTime:    t.Add(dur),
		EntryCount: 3,
	}
}

func TestTemporalOverlap_Overlapping(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	a := templateAt("a", "conn refused", base, 5*time.Minute, wirelogs.SeverityError)
	b := templateAt("b", "pool exhausted", base.Add(2*time.Minute), 5*time.Minute, wirelogs.SeverityError)

	score, ok := temporalOverlap(a, b, defaultCorrelationWindow)
	if !ok {
		t.Fatal("expected overlap, got none")
	}
	if score <= 0 || score > 1 {
		t.Errorf("score out of range: %f", score)
	}
}

func TestTemporalOverlap_Disjoint(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	a := templateAt("a", "x", base, time.Second, wirelogs.SeverityError)
	// b is an hour later — window/2 padding can't close the gap.
	b := templateAt("b", "y", base.Add(time.Hour), time.Second, wirelogs.SeverityError)

	_, ok := temporalOverlap(a, b, defaultCorrelationWindow)
	if ok {
		t.Fatal("expected disjoint, got overlap")
	}
}

func TestTemporalOverlap_PointEventsWithinWindow(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	a := templateAt("a", "x", base, 0, wirelogs.SeverityError)
	b := templateAt("b", "y", base.Add(20*time.Second), 0, wirelogs.SeverityError)

	score, ok := temporalOverlap(a, b, defaultCorrelationWindow)
	if !ok {
		t.Fatal("expected point events within window to overlap")
	}
	// Point events 20s apart with a 60s window pad out to [base-30s,
	// base+30s] and [base-10s, base+50s] — overlap is 40/60 ≈ 0.67.
	if score <= 0 || score > 1 {
		t.Errorf("score out of range: %f", score)
	}
}

func TestFindCorrelations_WithChecker_Confirmed(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	apiStream := streamFor("api")
	dbStream := streamFor("db")

	apiTmpl := templateAt("tpl-api-err", "conn refused <*>", base, 5*time.Minute, wirelogs.SeverityError)
	dbTmpl := templateAt("tpl-db-err", "pool exhausted <*>", base.Add(2*time.Minute), 5*time.Minute, wirelogs.SeverityError)

	chunks := []*wirelogs.LogChunk{
		chunkFor(apiStream, apiTmpl, base, 5*time.Minute),
		chunkFor(dbStream, dbTmpl, base.Add(2*time.Minute), 5*time.Minute),
	}

	proxyMap := map[string]string{
		"api": "proxy:cloud:default:arn:api",
		"db":  "proxy:cloud:default:arn:db",
	}
	resolver := resolverFromMap(map[string]string{
		"api": "proxy:cloud:default:arn:api",
		"db":  "proxy:cloud:default:arn:db",
	})
	checker := newFakeChecker([2]string{"proxy:cloud:default:arn:api", "proxy:cloud:default:arn:db"})

	results := findCorrelations(
		context.Background(),
		[]*wirelogs.LogTemplate{apiTmpl, dbTmpl},
		chunks,
		[]*wirelogs.LogStream{apiStream, dbStream},
		proxyMap,
		resolver,
		checker,
	)
	if len(results) != 1 {
		t.Fatalf("expected 1 correlation, got %d", len(results))
	}
	r := results[0]
	if !r.StructurallyConfirmed {
		t.Errorf("expected confirmed, got %+v", r)
	}
	if r.ServiceA == r.ServiceB {
		t.Errorf("expected cross-service correlation, got %s ↔ %s", r.ServiceA, r.ServiceB)
	}
	if r.ResourceA == "" || r.ResourceB == "" {
		t.Errorf("expected resource IDs populated, got %+v", r)
	}
	if checker.calls == 0 {
		t.Error("expected HasDependency to be invoked")
	}
}

func TestFindCorrelations_WithChecker_Unconfirmed(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	apiStream := streamFor("api")
	workerStream := streamFor("worker")

	apiTmpl := templateAt("tpl-api-err", "timeout <*>", base, 5*time.Minute, wirelogs.SeverityError)
	workerTmpl := templateAt("tpl-worker-err", "OOM killed", base.Add(time.Minute), 5*time.Minute, wirelogs.SeverityError)

	chunks := []*wirelogs.LogChunk{
		chunkFor(apiStream, apiTmpl, base, 5*time.Minute),
		chunkFor(workerStream, workerTmpl, base.Add(time.Minute), 5*time.Minute),
	}

	proxyMap := map[string]string{
		"api":    "proxy:cloud:default:arn:api",
		"worker": "proxy:cloud:default:arn:worker",
	}
	resolver := resolverFromMap(map[string]string{
		"api":    "proxy:cloud:default:arn:api",
		"worker": "proxy:cloud:default:arn:worker",
	})
	// Checker knows only about api↔db, not api↔worker.
	checker := newFakeChecker([2]string{"proxy:cloud:default:arn:api", "proxy:cloud:default:arn:db"})

	results := findCorrelations(
		context.Background(),
		[]*wirelogs.LogTemplate{apiTmpl, workerTmpl},
		chunks,
		[]*wirelogs.LogStream{apiStream, workerStream},
		proxyMap,
		resolver,
		checker,
	)
	if len(results) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(results))
	}
	if results[0].StructurallyConfirmed {
		t.Errorf("expected unconfirmed for api↔worker, got %+v", results[0])
	}
}

func TestFindCorrelations_NilChecker(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	apiStream := streamFor("api")
	dbStream := streamFor("db")
	apiTmpl := templateAt("a", "x <*>", base, 5*time.Minute, wirelogs.SeverityError)
	dbTmpl := templateAt("b", "y <*>", base.Add(time.Minute), 5*time.Minute, wirelogs.SeverityError)
	chunks := []*wirelogs.LogChunk{
		chunkFor(apiStream, apiTmpl, base, 5*time.Minute),
		chunkFor(dbStream, dbTmpl, base.Add(time.Minute), 5*time.Minute),
	}
	proxyMap := map[string]string{"api": "arn:api", "db": "arn:db"}

	results := findCorrelations(
		context.Background(),
		[]*wirelogs.LogTemplate{apiTmpl, dbTmpl},
		chunks,
		[]*wirelogs.LogStream{apiStream, dbStream},
		proxyMap,
		nil, // nil resolver
		nil, // nil checker
	)
	if len(results) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(results))
	}
	if results[0].StructurallyConfirmed {
		t.Errorf("nil checker should leave StructurallyConfirmed=false, got %+v", results[0])
	}
}

func TestFindCorrelations_NoErrorTemplates(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	apiStream := streamFor("api")
	dbStream := streamFor("db")
	infoA := templateAt("a", "served <*>", base, time.Minute, wirelogs.SeverityInfo)
	infoB := templateAt("b", "served <*>", base, time.Minute, wirelogs.SeverityInfo)
	chunks := []*wirelogs.LogChunk{
		chunkFor(apiStream, infoA, base, time.Minute),
		chunkFor(dbStream, infoB, base, time.Minute),
	}

	results := findCorrelations(
		context.Background(),
		[]*wirelogs.LogTemplate{infoA, infoB},
		chunks,
		[]*wirelogs.LogStream{apiStream, dbStream},
		map[string]string{},
		nil,
		nil,
	)
	if len(results) != 0 {
		t.Errorf("INFO templates must not correlate, got %d", len(results))
	}
}

func TestFindCorrelations_SameServicePairsSkipped(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	apiStream := streamFor("api")
	tplA := templateAt("a", "x <*>", base, time.Minute, wirelogs.SeverityError)
	tplB := templateAt("b", "y <*>", base, time.Minute, wirelogs.SeverityError)
	// Both templates owned by the same service.
	chunks := []*wirelogs.LogChunk{
		chunkFor(apiStream, tplA, base, time.Minute),
		chunkFor(apiStream, tplB, base, time.Minute),
	}

	results := findCorrelations(
		context.Background(),
		[]*wirelogs.LogTemplate{tplA, tplB},
		chunks,
		[]*wirelogs.LogStream{apiStream},
		map[string]string{"api": "arn:api"},
		resolverFromMap(map[string]string{"api": "arn:api"}),
		newFakeChecker(),
	)
	if len(results) != 0 {
		t.Errorf("same-service pairs must not correlate, got %d", len(results))
	}
}

func TestFindCorrelations_NoTemporalOverlap(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	apiStream := streamFor("api")
	dbStream := streamFor("db")
	// Windows an hour apart — no chance of overlap.
	apiTmpl := templateAt("a", "x <*>", base, time.Second, wirelogs.SeverityError)
	dbTmpl := templateAt("b", "y <*>", base.Add(time.Hour), time.Second, wirelogs.SeverityError)
	chunks := []*wirelogs.LogChunk{
		chunkFor(apiStream, apiTmpl, base, time.Second),
		chunkFor(dbStream, dbTmpl, base.Add(time.Hour), time.Second),
	}

	results := findCorrelations(
		context.Background(),
		[]*wirelogs.LogTemplate{apiTmpl, dbTmpl},
		chunks,
		[]*wirelogs.LogStream{apiStream, dbStream},
		map[string]string{"api": "arn:api", "db": "arn:db"},
		resolverFromMap(map[string]string{"api": "arn:api", "db": "arn:db"}),
		newFakeChecker([2]string{"arn:api", "arn:db"}),
	)
	if len(results) != 0 {
		t.Errorf("disjoint time ranges must not correlate, got %d", len(results))
	}
}

func TestServiceFromStream_Precedence(t *testing.T) {
	// service wins over namespace.
	both := &wirelogs.LogStream{Labels: map[string]string{
		wirelogs.FieldService:   "svc-wins",
		wirelogs.FieldNamespace: "ns-lose",
	}}
	if got := serviceFromStream(both); got != "svc-wins" {
		t.Errorf("service should take precedence, got %q", got)
	}

	// namespace used when service absent.
	nsOnly := &wirelogs.LogStream{Labels: map[string]string{wirelogs.FieldNamespace: "prod"}}
	if got := serviceFromStream(nsOnly); got != "prod" {
		t.Errorf("namespace fallback, got %q", got)
	}

	// nothing to match.
	empty := &wirelogs.LogStream{Labels: map[string]string{"irrelevant": "x"}}
	if got := serviceFromStream(empty); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
