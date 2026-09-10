// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// identity.go — THE TWO PRODUCER STAMPS a registered collect owes the
// incremental diff, and why each covers what it covers.
//
// THE DIFF REFUSES AN UNSTAMPED RESULT rather than reading it as unchanged, on
// both fields, and those refusals are the reason this file exists. Left unset,
// an empty DiscoveryFingerprint aborts the collect before the manifest fetch and
// a zero CollectorOutputVersion aborts it just after — so a registered custom
// collect could not reach the diff at all. The stamps are not decoration: each
// answers a question the per-key hashes CANNOT, because both describe a change
// that leaves every row's bytes identical.

// providerIdentity is the collector-build identity of a registered provider: a
// digest of WHO produces this graph's rows and WHICH tool on them is called.
//
// IT IS THE CUSTOM FAMILY'S ANALOG OF parser.CollectorOutputVersion, which the
// code collector bumps by hand when its emission changes. A registered
// collector's emission is not ours to version — the provider is somebody else's
// program — so the identity of the PROGRAM is the closest honest answer: point
// the registration at a different command, a different URL or a different tool
// and the rows may change in ways no contribution hash can see (node ids,
// summaries, metadata), which is exactly the class collectorVersionChange
// exists to catch. It re-lands the graph in full with the manifest echo
// suppressed, one collect, and then returns to the diff.
//
// IT DOES NOT COVER THE PROVIDER'S OWN VERSION, and that limit is stated rather
// than papered over: a provider that changes its emission without changing its
// command, URL or tool name is invisible here, exactly as a code collector whose
// author forgets to bump the constant is invisible there. The contract carries
// no provider-declared version field to read; adding one is a contract change,
// not a stamp.
//
// ZERO IS THE UNSTAMPED SENTINEL, so a digest whose low 32 bits happen to be
// zero is nudged to 1 rather than reported as "the collector did not stamp
// this". The nudge costs one indistinguishable pair in 2^32 and buys a field
// whose zero value keeps meaning exactly one thing.
func providerIdentity(def *knowledgev1.GraphTypeDef) (uint32, error) {
	col, err := collectorSpec(def)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	// The registration NAME is folded in as well as the provider, so two graph
	// types pointed at the same tool of the same provider still hold distinct
	// identities — the baseline is scoped per graph, but a collision here would
	// make a real re-registration invisible on whichever type inherited it.
	fmt.Fprintf(h, "name\x00%s\x00tool\x00%s\x00", def.GetName(), col.GetTool())
	switch {
	case col.GetStdio() != nil:
		s := col.GetStdio()
		fmt.Fprintf(h, "stdio\x00%s\x00", s.GetCommand())
		for _, a := range s.GetArgs() {
			fmt.Fprintf(h, "arg\x00%s\x00", a)
		}
		// THE ENV BLOCK'S KEYS ARE PART OF THE IDENTITY BECAUSE THEY DECIDE WHAT
		// THE PROVIDER CAN SEE. A provider handed a new variable legitimately emits
		// a different graph, and that difference moves no row's bytes in a way the
		// diff could detect.
		//
		// THE KEYS ONLY, NEVER THE VALUES, AND THAT IS A RULE RATHER THAN A
		// PRECAUTION. The elements arrive as NAME=value, so folding them whole
		// would digest the operator's secrets AND make a CREDENTIAL ROTATION change
		// the collector identity — which re-lands the whole graph, with the
		// manifest echo suppressed, for a change that alters no row's bytes. The
		// http arm rotates a header value and changes nothing, because it folds
		// only the URL; the two arms would otherwise disagree about the same event.
		//
		// SORTED, AND THE SORT IS LOAD-BEARING. The block is a JSON object decoded
		// into a Go map, whose iteration order is randomized. The loader already
		// emits the slice in sorted key order for exactly this reason; sorting again
		// here is the defensive half, because an unsorted fold gives one unchanged
		// entry a different identity on every collect and re-lands its graph every
		// time.
		//
		// DO NOT "FIX" THIS BY DROPPING ENV FROM THE FOLD. That blinds the stamp to
		// a real change: an operator who adds a variable the provider then reads
		// gets a different graph, and no contribution hash over the rows can see it.
		keys := make([]string, 0, len(s.GetEnv()))
		for _, e := range s.GetEnv() {
			name, _, _ := strings.Cut(e, "=")
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "env\x00%s\x00", k)
		}
	case col.GetHttp() != nil:
		fmt.Fprintf(h, "http\x00%s\x00", col.GetHttp().GetUrl())
	default:
		// A record with no provider arm cannot have been dialed, so reaching here
		// means the caller ran a collect against a record collectorSpec admitted
		// and the transport did not. It errors rather than digesting the empty
		// case, which would give every provider-less record one shared identity.
		return 0, fmt.Errorf(
			"custom collector: graph type %q names no provider transport, so its collector identity is undefined",
			def.GetName())
	}
	sum := h.Sum(nil)
	v := binary.BigEndian.Uint32(sum[:4])
	if v == 0 {
		return 1, nil
	}
	return v, nil
}

// discoveryFingerprint digests the collect PARAMS — the configuration this
// collect asked its provider for.
//
// IT IS THE CUSTOM FAMILY'S ANALOG OF THE CODE COLLECTOR'S DISCOVERY
// FINGERPRINT, and it guards the same failure. A code collect scoped by package
// prefixes emits nothing for the out-of-scope files, and every deletion guard
// would admit naming them: the walk was complete, the ratio is ordinary, each
// path has a live row. A registered collect scoped by its own params — a project
// key, a date window, a label filter — emits nothing for the records outside the
// window and is in exactly that position. Comparing this value against the
// previous collect's is what refuses the deletion.
//
// IT IS ALWAYS NON-EMPTY, including for a collect with NO params: the empty
// params object has a digest like any other, and "no scoping" is itself a
// discovery configuration that a later scoped collect must differ from. An
// empty return would trip the sink's unstamped-producer abort, which is a
// refusal for a fault this collect does not have.
//
// PARAMS ARE DIGESTED THROUGH JSON, whose map encoding sorts keys at every
// level, so the value is a function of the params rather than of Go's map
// iteration order. A params object that cannot be marshaled is an error: the
// same object is about to be sent to the provider as tool arguments, so a
// fingerprint that silently degraded would describe a collect that never ran.
func discoveryFingerprint(params map[string]any) (string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("custom collector: collect params are not JSON, so this collect has no discovery fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
