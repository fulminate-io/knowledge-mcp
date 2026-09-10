// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// runtime_test.go — the two records one entry produces, and the properties that
// make them two.

// TestRuntime_StdioEntryBuildsTheProviderOneof pins that a stdio entry becomes a
// CollectorSpec carrying the stdio provider and nothing else, with the env block
// rendered as NAME=value.
func TestRuntime_StdioEntryBuildsTheProviderOneof(t *testing.T) {
	reg := Runtime("tickets", Entry{
		Type: TransportStdio, Command: "/usr/local/bin/p", Args: []string{"--serve"},
		Env: map[string]string{"TOKEN": "t"}, Tool: "collect",
	})
	col := reg.Def.GetCollector()
	assert.Equal(t, "tickets", reg.Def.GetName(), "the entry name IS the graph family")
	assert.Equal(t, "collect", col.GetTool())
	require.NotNil(t, col.GetStdio())
	assert.Equal(t, "/usr/local/bin/p", col.GetStdio().GetCommand())
	assert.Equal(t, []string{"--serve"}, col.GetStdio().GetArgs())
	assert.Equal(t, []string{"TOKEN=t"}, col.GetStdio().GetEnv())
	assert.Nil(t, col.GetHttp(), "only one provider may be set")
	assert.Nil(t, reg.Headers, "a stdio entry carries no headers")
}

// TestRuntime_HTTPEntryCarriesHeadersBesideTheRecord pins the asymmetry the
// contract deliberately keeps: the url is ON the record and the headers are
// BESIDE it, so no header value can enter the digest the identity stamp reads or
// the record the server persists.
func TestRuntime_HTTPEntryCarriesHeadersBesideTheRecord(t *testing.T) {
	reg := Runtime("remote", Entry{
		Type: TransportHTTP, URL: "https://c.example/mcp", Tool: "collect_logs",
		Headers: map[string]string{"Authorization": "Bearer x"},
	})
	col := reg.Def.GetCollector()
	require.NotNil(t, col.GetHttp())
	assert.Equal(t, "https://c.example/mcp", col.GetHttp().GetUrl())
	assert.Nil(t, col.GetStdio())
	assert.Equal(t, map[string]string{"Authorization": "Bearer x"}, reg.Headers)
}

// TestRuntime_EnvIsEmittedSortedAndNonNil is the determinism row.
//
// SORTED: the block is a Go map and map iteration order is randomized, so an
// unsorted emission gives one unchanged entry a different collector identity on
// every collect and re-lands its whole graph each time. The assertion is on the
// exact slice rather than on its contents, because the ORDER is the property.
//
// NON-NIL: a nil Env means "inherit the parent's environment" to os/exec, which
// is the single behavior this whole contract removes.
func TestRuntime_EnvIsEmittedSortedAndNonNil(t *testing.T) {
	// TEN KEYS, NOT THREE, AND THE COUNT IS THE POINT. Three keys land in one
	// map bucket, so an unsorted emission is one of three ROTATIONS of that
	// bucket and one rotation IS the sorted order: measured, the unsorted
	// mutation passed this assertion on 2 runs in 8. Ten keys spread across more
	// than one bucket, so a map-order emission has to reproduce a full
	// permutation to pass, and the mutation reds every run.
	reg := Runtime("tickets", Entry{
		Type: TransportStdio, Command: "/p", Tool: "collect",
		Env: map[string]string{
			"K10": "10", "K02": "2", "K07": "7", "K01": "1", "K09": "9",
			"K04": "4", "K08": "8", "K03": "3", "K06": "6", "K05": "5",
		},
	})
	assert.Equal(t,
		[]string{"K01=1", "K02=2", "K03=3", "K04=4", "K05=5", "K06=6", "K07=7", "K08=8", "K09=9", "K10=10"},
		reg.Def.GetCollector().GetStdio().GetEnv())

	empty := Runtime("tickets", Entry{Type: TransportStdio, Command: "/p", Tool: "collect"})
	assert.NotNil(t, empty.Def.GetCollector().GetStdio().GetEnv(),
		"an entry with no env block must yield an EMPTY environment, never a nil one that os/exec reads as inheritance")
	assert.Empty(t, empty.Def.GetCollector().GetStdio().GetEnv())
}

// TestPersisted_CarriesNameAndBehaviorAndNoCollector is the secret-leak row.
// The persisted record crosses the wire and lands in a graph-resident node that
// syncs, so an env VALUE or a header value reaching it would store an operator's
// credential in the graph.
func TestPersisted_CarriesNameAndBehaviorAndNoCollector(t *testing.T) {
	d := Persisted("tickets", Entry{
		Type: TransportStdio, Command: "/usr/local/bin/p", Tool: "collect",
		Env: map[string]string{"TOKEN": "super-secret"},
	})
	assert.Equal(t, "tickets", d.GetName())
	assert.Nil(t, d.GetCollector(), "the persisted record carries NO connection half")
	require.NotNil(t, d.GetBehavior(), "and it does carry the behavior, which is the only half the server reads")
}

// TestPersisted_BehaviorDefaultsAreSetEXPLICITLY asserts BOTH halves of the
// default: that all three booleans are SET, and which value each takes.
//
// PRESENCE IS ASSERTED SEPARATELY FROM VALUE because the two say different
// things and the getters conflate them. GetSummarizable() returns false for a
// field that is unset AND for one explicitly set false, so a value-only assertion
// would pass for a record that wrote nothing at all — and "nobody said" is
// exactly what an operator reading `collector get` or the catalog must be able to
// tell apart from "this family opted out".
//
// SYNCABLE DEFAULTS ON; THE TWO LLM AXES DEFAULT OFF. Summarizing and embedding
// are LLM spend, so a family gets them because its entry asked. The family stays
// text-searchable either way: a registered graph is admitted to the keyword
// corpus regardless of its embed setting.
func TestPersisted_BehaviorDefaultsAreSetEXPLICITLY(t *testing.T) {
	d := Persisted("tickets", Entry{Type: TransportStdio, Command: "/p", Tool: "collect"})
	b := d.GetBehavior()
	require.NotNil(t, b)
	require.NotNil(t, b.Syncable, "syncable must be SET, not left for the server to coalesce to false")
	require.NotNil(t, b.Summarizable, "summarizable must be SET — an explicit false and an unset "+
		"field behave the same on the server but read differently to an operator")
	require.NotNil(t, b.Embeddable, "embeddable must be SET")
	assert.True(t, *b.Syncable, "syncable defaults ON: it costs no LLM call and an unsynced family "+
		"is invisible to every other machine")
	assert.False(t, *b.Summarizable, "summarization is LLM spend and is OPT-IN per collector")
	assert.False(t, *b.Embeddable, "embedding is LLM spend and is OPT-IN per collector")
}

// TestPersisted_ExplicitTrueOptsAnAxisIn is the inverse of the row above, and it
// is what keeps that row from being satisfied by a loader that hard-coded false.
func TestPersisted_ExplicitTrueOptsAnAxisIn(t *testing.T) {
	yes := true
	b := Persisted("tickets", Entry{
		Type: TransportStdio, Command: "/p", Tool: "collect",
		Behavior: &Behavior{Summarizable: &yes, Embeddable: &yes},
	}).GetBehavior()
	require.NotNil(t, b.Summarizable)
	require.NotNil(t, b.Embeddable)
	assert.True(t, *b.Summarizable, "an entry that asked for summaries gets them")
	assert.True(t, *b.Embeddable, "an entry that asked for embeddings gets them")
}

// TestPersisted_ExplicitFalseIsHonored is the other half: the default is a
// default, not an override. Without this row a loader that hard-coded true would
// pass the row above.
//
// ONE CASE PER FIELD, and the table is not tidiness. behaviorProto seeds all
// three true and then overrides each from the entry in its own branch, so a
// single-field test leaves the other two branches unobserved: deleting either
// left every package green while an entry writing that flag false reached the
// server as true. SYNCABLE IS THE COSTLIEST of the three — it gates the sync
// receive path and half the coverage row's eligibility — and it was one of the
// two that no test reached.
// flagWant pairs one untouched flag's pointer with the default it must still
// carry. The three no longer share one default, so the row cannot assert a single
// value across them.
type flagWant struct {
	p    *bool
	want bool
}

func TestPersisted_ExplicitFalseIsHonored(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name      string
		set       func(*Behavior, *bool)
		get       func(*knowledgev1.BehaviorDefaults) *bool
		restNames func(*knowledgev1.BehaviorDefaults) []flagWant
	}{
		{
			"syncable",
			func(b *Behavior, v *bool) { b.Syncable = v },
			func(d *knowledgev1.BehaviorDefaults) *bool { return d.Syncable },
			func(d *knowledgev1.BehaviorDefaults) []flagWant {
				return []flagWant{{d.Summarizable, false}, {d.Embeddable, false}}
			},
		},
		{
			"summarizable",
			func(b *Behavior, v *bool) { b.Summarizable = v },
			func(d *knowledgev1.BehaviorDefaults) *bool { return d.Summarizable },
			func(d *knowledgev1.BehaviorDefaults) []flagWant {
				return []flagWant{{d.Syncable, true}, {d.Embeddable, false}}
			},
		},
		{
			"embeddable",
			func(b *Behavior, v *bool) { b.Embeddable = v },
			func(d *knowledgev1.BehaviorDefaults) *bool { return d.Embeddable },
			func(d *knowledgev1.BehaviorDefaults) []flagWant {
				return []flagWant{{d.Syncable, true}, {d.Summarizable, false}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := &Behavior{}
			tc.set(block, &no)
			b := Persisted("tickets", Entry{
				Type: TransportStdio, Command: "/p", Tool: "collect", Behavior: block,
			}).GetBehavior()

			got := tc.get(b)
			require.NotNil(t, got, "%s must be SET, not dropped to unset", tc.name)
			assert.False(t, *got, "an explicit false must be carried through exactly as written")

			for i, other := range tc.restNames(b) {
				require.NotNil(t, other.p, "the flags the entry did not mention must still be set (%d)", i)
				assert.Equal(t, other.want, *other.p,
					"and they keep their own default: syncable ON, the two LLM axes OFF")
			}
		})
	}
}

// TestPersisted_FieldListsAndNodeTypeOverridesReachTheRecord pins the rest of
// the behavior block, including that an override's unset flag stays UNSET —
// inherit is the cascade's own rule at that level, and applying the graph
// default there would silently pin every node type.
func TestPersisted_FieldListsAndNodeTypeOverridesReachTheRecord(t *testing.T) {
	yes := true
	d := Persisted("tickets", Entry{
		Type: TransportStdio, Command: "/p", Tool: "collect",
		Behavior: &Behavior{
			EmbedFields:     []string{"description"},
			SummarizeFields: []string{"content"},
			Bm25Fields:      []string{"summary"},
			Extra:           map[string]string{"k": "v"},
		},
		NodeTypes: map[string]NodeTypeOverride{
			"issue": {Embeddable: &yes, Bm25Fields: []string{"title"}},
		},
	})
	b := d.GetBehavior()
	assert.Equal(t, []string{"description"}, b.GetEmbedFields())
	assert.Equal(t, []string{"content"}, b.GetSummarizeFields())
	assert.Equal(t, []string{"summary"}, b.GetBm25Fields())
	assert.Equal(t, map[string]string{"k": "v"}, b.GetExtra())

	ov := d.GetNodeTypes()["issue"]
	require.NotNil(t, ov)
	require.NotNil(t, ov.Embeddable)
	assert.True(t, *ov.Embeddable)
	assert.Nil(t, ov.Summarizable, "an override's unset flag stays UNSET: at this level unset means inherit")
	assert.Equal(t, []string{"title"}, ov.GetBm25Fields())
}
