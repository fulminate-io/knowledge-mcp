// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// declaration_test.go — the RECORD half of the describe contract: what a
// declaration puts on the entry, what rides to the server, and what deliberately
// does not.

// fullDeclaration is the declaration these rows fill from. Every half is set, so
// a merge that dropped one turns a row red rather than passing on a zero value.
func fullDeclaration() *externalcollector.Declaration {
	return &externalcollector.Declaration{
		Behavior: &externalcollector.DeclaredBehavior{
			Summarizable:    new(true),
			Embeddable:      new(true),
			Syncable:        new(true),
			EmbedFields:     []string{"summary", "content"},
			SummarizeFields: []string{"content"},
			Bm25Fields:      []string{"keywords"},
		},
		NodeTypeOverrides: map[string]externalcollector.DeclaredNodeTypeOverride{
			"issue": {Summarizable: new(false), EmbedFields: []string{"summary"}},
		},
		NodeTypes: []string{"issue", "epic"},
		EdgeTypes: []string{"blocks"},
		Environment: []externalcollector.DeclaredEnv{
			{Name: "ACME_HOME", Class: externalcollector.EnvClassPath},
			{Name: "ACME_REGION", Class: externalcollector.EnvClassSelector},
			{Name: "ACME_TOKEN", Class: externalcollector.EnvClassSecret},
			{Name: "ACME_PROXY", Class: externalcollector.EnvClassNotCarried},
		},
		Context: externalcollector.ContextDeclaration{
			"code": {NodeTypes: []string{"file"}, NodeFields: []string{"id"}},
		},
	}
}

// declaredEnvLookup builds an EnvLookup over a fixed map, so a row drives the three-class
// policy without touching the process environment.
func declaredEnvLookup(pairs map[string]string) EnvLookup {
	return func(name string) (string, bool) {
		v, ok := pairs[name]
		return v, ok
	}
}

// TestApplyDeclaration_FillsEveryDeclaredHalf is the merge's positive row.
func TestApplyDeclaration_FillsEveryDeclaredHalf(t *testing.T) {
	decl := fullDeclaration()
	got, _ := ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, decl, declaredEnvLookup(nil))

	require.NotNil(t, got.Vocabulary)
	assert.Equal(t, decl.NodeTypes, got.Vocabulary.NodeTypes)
	assert.Equal(t, decl.EdgeTypes, got.Vocabulary.EdgeTypes)
	assert.Equal(t, decl.Environment, got.EnvDeclaration)
	assert.Equal(t, decl.Context, got.Context)
	require.Contains(t, got.NodeTypes, "issue")
	assert.Equal(t, []string{"summary"}, got.NodeTypes["issue"].EmbedFields)
	require.NotNil(t, got.Behavior)
	assert.Equal(t, decl.Behavior.EmbedFields, got.Behavior.EmbedFields)
	assert.Equal(t, decl.Behavior.SummarizeFields, got.Behavior.SummarizeFields)
	assert.Equal(t, decl.Behavior.Bm25Fields, got.Behavior.Bm25Fields)
}

// TestApplyDeclaration_TheTwoLLMAxesAreNeverTakenFromTheDeclaration is the spend
// rule at this layer. The declaration suggests both ON; neither may be written.
func TestApplyDeclaration_TheTwoLLMAxesAreNeverTakenFromTheDeclaration(t *testing.T) {
	got, _ := ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, fullDeclaration(), declaredEnvLookup(nil))
	require.NotNil(t, got.Behavior)
	assert.Nil(t, got.Behavior.Summarizable, "summarize is the operator's, and a declaration must not set it")
	assert.Nil(t, got.Behavior.Embeddable, "embed is the operator's, and a declaration must not set it")

	// And an operator's own value survives the merge untouched.
	withFlags := Entry{Type: TransportStdio, Tool: "collect", Behavior: &Behavior{Summarizable: new(true)}}
	got, _ = ApplyDeclaration(withFlags, fullDeclaration(), declaredEnvLookup(nil))
	require.NotNil(t, got.Behavior.Summarizable)
	assert.True(t, *got.Behavior.Summarizable)
}

// TestApplyDeclaration_ADeclarationWithNothingToAddWritesNoBehaviorKey pins the
// absence the whole block's meaning rests on.
func TestApplyDeclaration_ADeclarationWithNothingToAddWritesNoBehaviorKey(t *testing.T) {
	bare := &externalcollector.Declaration{
		Behavior:    &externalcollector.DeclaredBehavior{Summarizable: new(true), Embeddable: new(true), Syncable: new(true)},
		NodeTypes:   []string{},
		EdgeTypes:   []string{},
		Environment: []externalcollector.DeclaredEnv{},
	}
	got, _ := ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, bare, declaredEnvLookup(nil))
	assert.Nil(t, got.Behavior,
		"a declared syncable=true is the loader's own default and writes nothing; the block's absence is what records that the operator said nothing")
	require.NotNil(t, got.Vocabulary, "the vocabulary is still PRESENT, declaring that this collector emits nothing")
	assert.Empty(t, got.Vocabulary.NodeTypes)
}

// TestApplyDeclaration_ADeclaredSyncableFalseIsWritten is the other half: a
// departure from the default is a real statement and is recorded.
func TestApplyDeclaration_ADeclaredSyncableFalseIsWritten(t *testing.T) {
	decl := fullDeclaration()
	decl.Behavior.Syncable = new(false)
	got, _ := ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, decl, declaredEnvLookup(nil))
	require.NotNil(t, got.Behavior)
	require.NotNil(t, got.Behavior.Syncable)
	assert.False(t, *got.Behavior.Syncable)
}

// TestApplyDeclaration_TheThreeClassEnvironmentPolicy walks every cell of the
// class matrix at the unit level, including the two unset arms.
func TestApplyDeclaration_TheThreeClassEnvironmentPolicy(t *testing.T) {
	env := declaredEnvLookup(map[string]string{
		"ACME_HOME":   "/home/acme",
		"ACME_REGION": "eu-west-2",
		"ACME_TOKEN":  "s3cret",
		"ACME_PROXY":  "http://proxy.internal:3128",
	})
	got, skipped := ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, fullDeclaration(), env)
	assert.Equal(t, "/home/acme", got.Env["ACME_HOME"])
	assert.Equal(t, "eu-west-2", got.Env["ACME_REGION"])
	assert.NotContains(t, got.Env, "ACME_TOKEN")
	// THE FOURTH CLASS, WITH ITS VARIABLE SET IN THE ENVIRONMENT, which is the
	// only state in which it can go wrong: a not-carried name writes nothing AND
	// is not reported as withheld. The second half is what distinguishes it from
	// the secret class — a credential the installer refused is news an operator
	// acts on, and a name the collector's author decided no entry should declare
	// is not, so reporting it would bury the credential line under a list nobody
	// reads.
	assert.NotContains(t, got.Env, "ACME_PROXY",
		"a not-carried name is written in no state, and this one IS set in the environment")
	assert.NotContains(t, skipped, "ACME_PROXY",
		"and it is not reported as a withheld credential; only the secret class is")
	assert.Equal(t, []string{"ACME_TOKEN"}, skipped, "the withheld NAME is reported, never the value")
	for _, v := range got.Env {
		assert.NotContains(t, v, "s3cret", "no secret value may reach the env block by any route")
	}

	// Unset: a path and a selector are omitted rather than written blank, and an
	// unset secret is not reported as skipped because nothing was withheld.
	got, skipped = ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, fullDeclaration(), declaredEnvLookup(nil))
	assert.Nil(t, got.Env, "an entry that derived nothing carries no env key at all")
	assert.Empty(t, skipped)

	// Set-but-empty is treated as unset for both writing classes: an empty
	// selector selects the thing named by the empty string.
	got, _ = ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, fullDeclaration(),
		declaredEnvLookup(map[string]string{"ACME_REGION": ""}))
	assert.NotContains(t, got.Env, "ACME_REGION")
}

// TestApplyDeclaration_AnHTTPEntryDerivesNoEnvBlock pins the transport rule: the
// env block is the stdio child's whole environment, and the loader refuses one
// on an http entry by name — so deriving one would write a file the next load
// rejects.
func TestApplyDeclaration_AnHTTPEntryDerivesNoEnvBlock(t *testing.T) {
	env := declaredEnvLookup(map[string]string{"ACME_HOME": "/home/acme", "ACME_REGION": "eu-west-2"})
	got, skipped := ApplyDeclaration(Entry{Type: TransportHTTP, Tool: "collect", URL: "https://x"}, fullDeclaration(), env)
	assert.Nil(t, got.Env)
	assert.Empty(t, skipped)
	assert.Len(t, got.EnvDeclaration, len(fullDeclaration().Environment),
		"the DECLARATION still rides, whole: what a remote collector reads is true about it whatever transport serves it")
	require.NoError(t, ValidateEntry("/tmp/x.json", "acme", got),
		"and the filled http entry must still pass the loader that will read it next")
}

// TestPersisted_CarriesTheVocabularyAndNotTheEnvDeclaration is R3's wire row,
// asserted on the MARSHALED BYTES rather than on a Go struct: a field that
// reached the record would be visible there whatever an accessor reported.
func TestPersisted_CarriesTheVocabularyAndNotTheEnvDeclaration(t *testing.T) {
	entry, _ := ApplyDeclaration(Entry{Type: TransportStdio, Tool: "collect"}, fullDeclaration(), declaredEnvLookup(nil))
	def := Persisted("acme", entry)

	require.NotNil(t, def.GetVocabulary(), "the vocabulary is what the server's ingest refusal reads")
	assert.Equal(t, []string{"issue", "epic"}, def.GetVocabulary().GetNodeTypes())
	assert.Equal(t, []string{"blocks"}, def.GetVocabulary().GetEdgeTypes())

	raw, err := proto.Marshal(def)
	require.NoError(t, err)
	blob := string(raw)
	for _, name := range []string{"ACME_HOME", "ACME_REGION", "ACME_TOKEN"} {
		assert.NotContains(t, blob, name,
			"the env DECLARATION is entry-only: %q reached the persisted record, which has no reader for it", name)
	}
	assert.NotContains(t, blob, "path_basenames", "and neither does the context declaration")
	assert.Nil(t, def.GetCollector(), "and the record still carries no connection half")
}

// TestPersisted_PresenceSurvivesTheRoundTrip is the presence matrix at the
// client end. The three states must stay three states through marshal and
// unmarshal, or the server cannot tell them apart either.
func TestPersisted_PresenceSurvivesTheRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name           string
		vocabulary     *Vocabulary
		wantPresent    bool
		wantNodeTypes  []string
		wantEmptyLists bool
	}{
		{"absent: an entry written before describe existed", nil, false, nil, false},
		{"declared and empty", &Vocabulary{NodeTypes: []string{}, EdgeTypes: []string{}}, true, nil, true},
		{"declared non-empty", &Vocabulary{NodeTypes: []string{"issue"}, EdgeTypes: []string{"blocks"}}, true, []string{"issue"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := Persisted("acme", Entry{Type: TransportStdio, Tool: "collect", Vocabulary: tc.vocabulary})
			raw, err := proto.Marshal(def)
			require.NoError(t, err)
			var decoded knowledgev1.GraphTypeDef
			require.NoError(t, proto.Unmarshal(raw, &decoded))

			got := decoded.GetVocabulary()
			if !tc.wantPresent {
				assert.Nil(t, got, "an absent vocabulary must decode as ABSENT, which is the accept-all arm")
				return
			}
			require.NotNil(t, got, "a declared vocabulary must survive as PRESENT even when it is empty")
			if tc.wantEmptyLists {
				assert.Empty(t, got.GetNodeTypes())
				return
			}
			assert.Equal(t, tc.wantNodeTypes, got.GetNodeTypes())
		})
	}
}

// TestApplyDeclaration_ANilDeclarationChangesNothing is the defensive row: the
// merge is called on one path only, and a nil there must not blank an entry.
func TestApplyDeclaration_ANilDeclarationChangesNothing(t *testing.T) {
	in := Entry{Type: TransportStdio, Tool: "collect", Env: map[string]string{"A": "1"}}
	got, skipped := ApplyDeclaration(in, nil, declaredEnvLookup(nil))
	assert.Equal(t, in, got)
	assert.Empty(t, skipped)
}
