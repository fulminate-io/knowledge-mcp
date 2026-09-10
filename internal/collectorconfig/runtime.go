// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// runtime.go — the two records one entry produces, and why they are two.

// Runtime builds the in-memory record ONE COLLECT is driven from. It never
// crosses the wire.
//
// IT CARRIES A FULLY POPULATED CollectorSpec, and that is forced rather than
// convenient: RunMCP reads the connection half off the record, collectorSpec
// errors on a nil collector and on an empty tool, and providerIdentity errors on
// a record naming no provider transport. A behavior-only record handed to the
// collect path fails all three.
//
// THE ENV BLOCK IS RENDERED AS NAME=value IN SORTED KEY ORDER. NAME=value is
// what os/exec wants. SORTED is a determinism property of the emitted record
// rather than a guard the rest of the system leans on, and saying which is which
// matters: os/exec does not care about the order of a duplicate-free environment
// slice, and the collector identity does not read this order either —
// providerIdentity builds its own key slice and sorts it before folding
// (identity.go), so the identity survives an unsorted emission. What sorting
// buys is a record that is byte-identical across runs for one unchanged entry,
// which is what makes the emission diffable and the fixture assertable. The
// block is a Go map whose iteration order is randomized, so without the sort
// that property is gone. The guard is TestRuntime_EnvIsEmittedSortedAndNonNil,
// whose fixture carries enough keys that a map-order emission cannot reproduce
// the sorted one by chance.
//
// THE HEADERS DO NOT RIDE THE PROTO RECORD. HttpProvider carries a url and
// nothing else, and this ticket changes no proto. They ride the Registration
// beside it, are set by the transport on every request, and therefore never
// enter the record the identity digest reads — so no header value is ever
// digested and none is ever persisted.
func Runtime(name string, e Entry) *externalcollector.Registration {
	col := &knowledgev1.CollectorSpec{Tool: e.Tool}
	switch e.Type {
	case TransportStdio:
		col.Provider = &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
			Command: e.Command,
			Args:    e.Args,
			Env:     envSlice(e.Env),
		}}
	case TransportHTTP:
		col.Provider = &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: e.URL}}
	}
	return &externalcollector.Registration{
		Def: &knowledgev1.GraphTypeDef{
			Name: name, Collector: col, Behavior: behaviorProto(e.Behavior),
			NodeTypes: nodeTypesProto(e.NodeTypes), Vocabulary: vocabularyProto(e.Vocabulary),
		},
		Headers: e.Headers,
		Context: e.Context,
	}
}

// envSlice renders the env block as the NAME=value elements os/exec takes, in
// sorted key order — see Runtime's comment for what the sort does and does not
// protect. It returns a NON-NIL slice for an empty or absent block: a nil Env
// means "inherit the parent's environment" to os/exec, which is the single
// behavior this whole contract exists to remove.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Persisted builds the record the loader upserts to the server: the family NAME
// and its BEHAVIOR, and NOTHING ELSE.
//
// THREE REASONS IT CARRIES NO CollectorSpec, in order of weight. The connection
// half is client-only — no server file reads it. The env block now carries
// VALUES, so persisting it would put an operator's secrets in a graph-resident
// node that syncs. And the server needs exactly one thing from this record: the
// behavior cascade, which its routing gate requires to exist at all and which
// its whole summarize / embed / sync pipeline keys on.
//
// IT CARRIES NO CONTEXT DECLARATION EITHER, and that absence is structural. The
// declaration says what the CLIENT reads out of its own graphs before it calls a
// provider; the server holds no domain knowledge about what a collector needs
// and has nothing to do with it. This is why the whole context feature moves no
// proto message and no generated code: the record the server sees is unchanged.
func Persisted(name string, e Entry) *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name:       name,
		Behavior:   behaviorProto(e.Behavior),
		NodeTypes:  nodeTypesProto(e.NodeTypes),
		Vocabulary: vocabularyProto(e.Vocabulary),
	}
}

// vocabularyProto converts the entry's declared type vocabulary.
//
// NIL STAYS NIL, AND THAT ABSENCE IS THE RECORD'S THIRD STATE. An entry written
// before the describe tool existed declares no vocabulary; the record it
// produces carries no vocabulary message; the server reads that as accept-all
// and the client says so once per collect. An entry declaring EMPTY lists
// produces a PRESENT message with empty lists, which refuses every type. A
// conversion that rendered nil as an empty message would collapse the two and
// silently refuse every node of every family registered before this change.
func vocabularyProto(v *Vocabulary) *knowledgev1.TypeVocabulary {
	if v == nil {
		return nil
	}
	return &knowledgev1.TypeVocabulary{
		NodeTypes: v.NodeTypes,
		EdgeTypes: v.EdgeTypes,
	}
}

// behaviorProto converts the entry's behavior block, writing an EXPLICIT value
// for each of the three booleans: syncable defaults ON, and the two LLM axes
// default OFF.
//
// THE POINTERS ARE THE POINT. The proto booleans are optional for presence, and
// presence is what distinguishes "the operator declared this" from "nobody
// said"; new(v) takes the address of the value rather than of a shared variable,
// so each field carries its own.
//
// THE THREE ARE SET EXPLICITLY, NEVER LEFT UNSET, and that has not changed. What
// changed is WHICH default two of them take. An unset boolean reaching the server
// coalesces to false anyway, so leaving them unset would produce the same
// behavior by accident; writing them means an operator reading `collector get` or
// the catalog sees what this family will actually do rather than a blank.
//
// SUMMARIZATION AND EMBEDDING ARE OPT-IN, PER COLLECTOR. They are LLM spend on
// somebody else's data, and whether a given family is worth summarizing or
// embedding is not a question this loader can answer for every collector anyone
// installs. So a family gets them because its entry asked, never because nobody
// said otherwise.
//
// THE OLD REASON FOR DEFAULTING THEM TRUE NO LONGER HOLDS. It was that a family
// with both axes off is collected, readable by id and walkable, and invisible to
// SEARCH forever — which was true when BM25 admission was gated on embed
// eligibility. It is not true now: a registered family is admitted to the keyword
// corpus regardless of its embed setting, so a CLI-default collector is
// text-searchable from its first collect. What an opted-out family loses is
// summaries and vectors, which is semantic ranking, not findability.
//
// SYNCABLE KEEPS ITS TRUE DEFAULT and is not part of this. It costs no LLM call,
// and a family that does not sync is invisible to every other machine, which is
// not a state anyone installs a collector to reach.
func behaviorProto(b *Behavior) *knowledgev1.BehaviorDefaults {
	out := &knowledgev1.BehaviorDefaults{
		Syncable:     new(true),
		Summarizable: new(false),
		Embeddable:   new(false),
	}
	if b == nil {
		return out
	}
	if b.Syncable != nil {
		out.Syncable = new(*b.Syncable)
	}
	if b.Summarizable != nil {
		out.Summarizable = new(*b.Summarizable)
	}
	if b.Embeddable != nil {
		out.Embeddable = new(*b.Embeddable)
	}
	out.EmbedFields = b.EmbedFields
	out.SummarizeFields = b.SummarizeFields
	out.Bm25Fields = b.Bm25Fields
	out.Extra = b.Extra
	return out
}

// nodeTypesProto converts the per-node-type overrides. Unset stays unset here:
// an override's absent flag means INHERIT the graph default, which is the
// server's own rule for these two, so the graph-level default (syncable ON,
// the two LLM axes OFF) does not apply at this level.
func nodeTypesProto(in map[string]NodeTypeOverride) map[string]*knowledgev1.NodeTypeOverride {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*knowledgev1.NodeTypeOverride, len(in))
	for nt, ov := range in {
		out[nt] = &knowledgev1.NodeTypeOverride{
			Summarizable:    ov.Summarizable,
			Embeddable:      ov.Embeddable,
			EmbedFields:     ov.EmbedFields,
			SummarizeFields: ov.SummarizeFields,
			Bm25Fields:      ov.Bm25Fields,
		}
	}
	return out
}
