// SPDX-License-Identifier: Apache-2.0

// Package workingset holds the set of graphs this client process has been asked
// to work with, and it is the gate every background loop consults before doing
// anything to a graph.
//
// THE RULE IT ENFORCES: there must not be any background process in the client
// process that requests or interacts with graphs in any way unless some kind of
// mcp query like search, mutate, collect has interacted with it directly.
// Management operations do not count towards interaction.
//
// Membership is therefore EARNED BY DIRECT USER INTERACTION and by nothing else
// — not by a graph existing on disk, not by it existing in the account, not by
// any background loop's own traffic. A graph nobody interacts with stops being
// enriched and stops having segments published, and that is the intended
// consequence rather than a regression to compensate for. There is deliberately
// no re-admission-on-behalf-of machinery anywhere in this package.
//
// Membership is IN-MEMORY and PER-PROCESS, empty until the first interaction,
// with no age-out and no seeded exceptions. A durable ledger would be a second
// way for an untouched graph to stay admitted forever, which is the class this
// package exists to kill; a process restart IS the age-out. The cost is stated
// rather than hidden: after a daemon restart nothing is maintained until the
// first user interaction, and the first search / query / collect / write
// re-admits within seconds of real work.
//
// DEFAULT-DENY IS STRUCTURAL. Every consumer treats a nil or absent *Set as
// EMPTY, never as unrestricted, so a missed wiring under-admits (safe, visible)
// rather than silently restoring account-wide behavior.
//
// The package is a LEAF: it depends only on kgtypes and the standard library, so
// pipeline, tools and bootstrap can all import it without a cycle.
package workingset

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Ref identifies one graph instance: its type plus its instance name (a repo, an
// account, a language, or a graph name). Always produced by Normalize, never
// built by hand, so every member is comparable on the same key shape.
type Ref struct {
	GraphType kgtypes.GraphType
	Name      string
}

// admission records why and when a Ref entered the set. The reason is the
// interaction that admitted it ("search", "collect", the operation term), kept
// so an operator reading a log or a future diagnostic can tell which kind of
// interaction earned a graph its place.
type admission struct {
	at     time.Time
	reason string
}

// waiter is one registered wake channel plus the filter that decides which
// admissions reach it. ref is meaningful only when any is false: Wake registers
// any=true (every first admission), WakeFor registers any=false with the one Ref
// it cares about.
type waiter struct {
	ch  chan struct{}
	ref Ref
	any bool
}

// Set is the working set. The zero value is not usable — call New. A nil *Set is
// usable and reads as EMPTY.
type Set struct {
	mu      sync.Mutex
	members map[Ref]admission
	waiters []waiter
}

// New returns an empty working set. Empty-until-interaction is the initial state
// on every process start, including for knowledge/default: nothing is seeded.
func New() *Set {
	return &Set{members: make(map[Ref]admission)}
}

// Admit records an interaction with (gt, name) and reports whether this was the
// FIRST admission of that Ref, so the caller can log the new member exactly once
// instead of on every search.
//
// It is on the search and write hot paths: one map lookup under an uncontended
// mutex, with no allocation on the already-a-member branch. A name Normalize
// refuses admits nothing and returns false. nil-safe.
func (s *Set) Admit(gt kgtypes.GraphType, name, reason string) bool {
	if s == nil {
		return false
	}
	ref, ok := Normalize(gt, name)
	if !ok {
		return false
	}
	s.mu.Lock()
	if _, exists := s.members[ref]; exists {
		s.mu.Unlock()
		return false
	}
	s.members[ref] = admission{at: time.Now(), reason: reason}
	s.mu.Unlock()

	s.signal(ref)
	return true
}

// Has reports whether (gt, name) is a member. This is the predicate every
// background loop is gated on. A nil *Set reports false for everything — EMPTY,
// never unrestricted.
func (s *Set) Has(gt kgtypes.GraphType, name string) bool {
	if s == nil {
		return false
	}
	ref, ok := Normalize(gt, name)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.members[ref]
	return exists
}

// Members returns every member, sorted by (GraphType, Name) so a reconcile walk
// and a test see the same order every time. It allocates and sorts once per
// pass, which is correct for a set bounded by the graphs this machine actually
// uses. nil-safe (returns nil).
func (s *Set) Members() []Ref {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]Ref, 0, len(s.members))
	for ref := range s.members {
		out = append(out, ref)
	}
	s.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].GraphType != out[j].GraphType {
			return out[i].GraphType < out[j].GraphType
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Wake REGISTERS a new waiter and returns its channel, signaled on every first
// admission, so a loop that would otherwise sleep out its interval can start
// working on a freshly admitted graph immediately.
//
// EACH CALL RETURNS ITS OWN CHANNEL and every waiter gets every signal. One
// shared channel would not do: several independent loops consume this, and a
// single coalescing channel would let whichever loop woke first swallow the
// signal the others needed. Call it ONCE at wiring time and hold the channel; a
// channel registered after an admission does not carry that admission.
//
// Receives are coalescing per waiter: several admissions during one sleep
// deliver one wake, and the receiver re-reads Members rather than treating the
// signal as a payload. A nil *Set returns a nil channel, which blocks forever in
// a select — the correct behavior when nothing can ever be admitted.
//
// A consumer whose work targets exactly ONE graph wants WakeFor instead, so an
// unrelated graph's admission does not start a pass that has nothing to do.
func (s *Set) Wake() <-chan struct{} {
	if s == nil {
		return nil
	}
	// Capacity 1 so a burst of admissions coalesces into one wake rather than
	// blocking the admitting call on a consumer that is mid-pass.
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.waiters = append(s.waiters, waiter{ch: ch, any: true})
	s.mu.Unlock()
	return ch
}

// WakeFor registers a waiter signaled ONLY when the named graph is first admitted,
// for a consumer whose work targets exactly one graph and for which a wake on any
// OTHER graph is pure amplification — the propagation family, every read of which
// targets knowledge/default. It is a SIBLING of Wake, not a replacement: Wake keeps
// its any-graph broadcast for the consumers that legitimately react to any
// admission (the pipeline catalog refresh, pipeline_refresh.go:77, and the deferred
// instruction bootstrap, client_workingset.go:121).
//
// Same registration contract as Wake: call it ONCE at wiring time and hold the
// channel; a channel registered after an admission does not carry that admission.
// A name Normalize refuses, and a nil *Set, return a nil channel — which blocks
// forever in a select, the correct default-deny behavior when nothing can ever be
// admitted.
//
// The filter key comes from the SAME Normalize every admission and membership test
// uses, so knowledge "" and "default" cannot become two different filters.
func (s *Set) WakeFor(gt kgtypes.GraphType, name string) <-chan struct{} {
	if s == nil {
		return nil
	}
	ref, ok := Normalize(gt, name)
	if !ok {
		return nil
	}
	// Capacity 1, coalescing, for the same reason Wake buffers: an admission must
	// never block on a consumer that is mid-pass.
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.waiters = append(s.waiters, waiter{ch: ch, ref: ref})
	s.mu.Unlock()
	return ch
}

// signal delivers a non-blocking wake for the just-admitted ref to every waiter
// that asked for it: the unfiltered ones registered by Wake, plus the WakeFor
// waiters registered against exactly this Ref. A full buffer means a wake is
// already pending and unread for that waiter, which carries the same information.
func (s *Set) signal(ref Ref) {
	s.mu.Lock()
	waiters := append([]waiter(nil), s.waiters...)
	s.mu.Unlock()
	for _, w := range waiters {
		if !w.any && w.ref != ref {
			continue
		}
		select {
		case w.ch <- struct{}{}:
		default:
		}
	}
}

// Normalize is the SINGLE normalization site for working-set keys, and every
// admission and every membership test goes through it so the two can never
// disagree about what a graph is called. It reports ok=false for a target that
// names no concrete graph instance, which is what makes a type-only enumeration
// unable to admit what it enumerates.
//
// Two rules, both read off the code that already depends on them:
//
//  1. BRANCH-STRIP. An overlay-qualified "repo@branch" normalizes to its bare
//     base name. The pipeline registers exactly ONE collector per BASE code-graph
//     name and branch qualification happens per item, so a branch-qualified
//     member could never equal a registered collector's name nor a reconcile ref:
//     the gate would be permanently inert while its own tests stayed green.
//
//  2. DEFAULT-INSTANCE FOR A SINGLE-INSTANCE FAMILY. The knowledge graph is
//     single-instance, and its instance name is written both as "" (the Stats
//     selector) and as "default" (the reconcile seed) in existing code.
//     Collapsing "" to "default" here is what stops those two spellings becoming
//     two different members — a drift this area has already produced once.
//     The checks graph is the same case reached from the other direction: its
//     selector policy declares it carries NO instance field and REJECTS a set
//     name, so every read of it sends "" and there is no other spelling it could
//     send. Both are listed in singleInstanceGraphs below.
//
// AN EMPTY NAME MEANS TWO OPPOSITE THINGS AND THE LIST IS WHAT TELLS THEM APART.
// For code / cloud / practice it means the caller named no repo, account or
// language — a catalog enumeration, which must admit nothing, and that refusal is
// the structural half of the admission gate. For a family with no instance field
// to leave empty, it is not an absent selector at all: it IS the one instance.
// Conflating the two is what left the checks graph permanently outside the
// working set, so the catalog loop registered no collector for it and its nodes
// stayed unembedded through every drain while the operation half of the gate
// passed and made the failure look like a routing question.
//
// Any other type with an empty instance name resolves nothing and is refused.
func Normalize(gt kgtypes.GraphType, name string) (Ref, bool) {
	base, _, _ := strings.Cut(strings.TrimSpace(name), "@")
	if singleInstanceGraphs[gt] {
		if base == "" {
			base = DefaultInstanceName
		}
		return Ref{GraphType: gt, Name: base}, true
	}
	if gt == "" || base == "" {
		return Ref{}, false
	}
	return Ref{GraphType: gt, Name: base}, true
}

// singleInstanceGraphs names every graph type whose empty instance name IS its
// one instance rather than an absent selector.
//
// IT IS ENUMERATED HERE RATHER THAN DERIVED FROM graphsel BECAUSE THIS PACKAGE IS
// A LEAF — its own doc states it depends only on kgtypes and the standard
// library, so pipeline, tools and bootstrap can all import it without a cycle,
// and importing the selector package to ask InstanceField would retire that
// property for one predicate. The cost is a second place the fact is written, and
// it is paid down by a drift guard rather than by a comment: a test that CAN see
// both packages requires every family graphsel classifies as carrying no instance
// field to normalize here, so a future singleton cannot be added on one side
// alone.
var singleInstanceGraphs = map[kgtypes.GraphType]bool{
	kgtypes.GraphKnowledge: true,
	kgtypes.GraphChecks:    true,
}

// DefaultInstanceName is the canonical instance a single-instance family is keyed
// under everywhere inside this process.
const DefaultInstanceName = "default"

// CanonicalInstanceName returns the name (gt, name) is KEYED UNDER INTERNALLY —
// by the per-graph segment engine, the collector registry and the working set.
//
// IT IS NOT A WIRE NAME, and the distinction is the whole reason this exists as a
// separate helper rather than as "just call Normalize". A graph has TWO names and
// they are not always the same string: the one a CALLER may legally put on a
// selector, and the one this process keys its own maps by. For the checks graph
// they DIVERGE — the server's selector policy carries no instance field and
// REJECTS a set name, so every wire read must send "", while the collector seals
// its segments under the canonical instance. A seam that used one name for both
// searched an engine instance nothing had ever written to and returned a
// confident zero; that conflation has now surfaced at three separate seams, so
// the two namespaces are named apart here rather than left to each call site.
//
// IT DELIBERATELY DOES NOT STRIP AN OVERLAY SUFFIX, which is where it parts
// company with Normalize. Normalize cuts a "repo@branch" name at the "@" because
// working-set membership is per BASE graph; an overlay-qualified SEARCH, by
// contrast, means to address the overlay, and canonicalizing it would silently
// redirect the read to the base pool. The only transformation applied here is the
// single-instance empty-name resolution.
//
// Every other (gt, name) is returned UNCHANGED, so this is safe to apply at any
// seam: for a family that carries a real instance field it is the identity.
func CanonicalInstanceName(gt kgtypes.GraphType, name string) string {
	if name == "" && singleInstanceGraphs[gt] {
		return DefaultInstanceName
	}
	return name
}
