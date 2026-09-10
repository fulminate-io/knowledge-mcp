// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collector_behavior_flags_test.go — R4's five flags: the two behavior booleans
// and the three field lists.
//
// THE TRI-STATE IS THE WHOLE POINT OF THE BOOLEAN HALF. The entry's booleans are
// POINTERS so an omitted key stays distinguishable from an explicit false, and
// the opt-in default is what makes that distinction load-bearing: "the operator
// did not mention it" and "the operator turned it off" reach the same server
// behavior but read completely differently to whoever inherits the file. A plain
// flag.Bool cannot report whether it was SET, so it would erase exactly that.
//
// THE BYTES ARE THE ASSERTION, not a decoded struct, for the reason the header
// and env flags already follow: an absent block must stay ABSENT in the written
// file, and a decoded struct cannot tell an absent key from a zero value.

// addedEntryBytes runs one `collector add` against a temp home, with the behavior
// options under test spliced in, and returns the raw JSON object written for that
// entry.
//
// IT USES THE HTTP TRANSPORT AND A LIVE CONTRACT PROVIDER because `add` DIALS
// before it writes: a case that named a command nobody can execute would fail at
// the dial and never reach the write these rows are about.
func addedEntryBytes(t *testing.T, opts []string, stub ...describeStubOption) (map[string]json.RawMessage, []byte) {
	t.Helper()
	userPath := useTempHome(t)
	useScratchCwd(t)
	url := startContractProvider(t, true, stub...)
	argv := append([]string{"-t", "http", "--tool", contractStubTool}, opts...)
	argv = append(argv, "tickets", url)
	var out bytes.Buffer
	require.NoError(t, runCollectorAdd(argv, &out))

	raw, err := os.ReadFile(userPath)
	require.NoError(t, err)

	var file struct {
		Collectors map[string]map[string]json.RawMessage `json:"collectors"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.Len(t, file.Collectors, 1)
	for _, entry := range file.Collectors {
		return entry, raw
	}
	return nil, raw
}

func behaviorBlock(t *testing.T, entry map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	blob, ok := entry["behavior"]
	if !ok {
		return nil
	}
	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(blob, &out))
	return out
}

// TestCollectorAdd_BehaviorBooleansAreTriState is the three-case-per-boolean row:
// absent writes NO key; an explicit false writes false; an explicit true writes
// true.
func TestCollectorAdd_BehaviorBooleansAreTriState(t *testing.T) {
	for _, flagPair := range []struct{ flag, key string }{
		{"--summarizable", "summarizable"},
		{"--embeddable", "embeddable"},
	} {
		t.Run(flagPair.key+" absent writes no key", func(t *testing.T) {
			// AGAINST A PROVIDER THAT DECLARES NOTHING BUT THE THREE BOOLEANS, so
			// the absence under test is the operator's silence rather than a
			// declaration that happens to carry the same word somewhere else in the
			// file. The declaring case is the row below it: a collector may SUGGEST
			// these two, and the suggestion still reaches no key.
			entry, raw := addedEntryBytes(t, nil, withBareDeclaration())
			assert.NotContains(t, string(raw), flagPair.key,
				"an absent flag must leave the key out of the file entirely — that absence is what "+
					"a later reader sees as 'nobody said'")
			assert.Nil(t, behaviorBlock(t, entry)[flagPair.key])
		})

		t.Run(flagPair.key+" absent stays absent even when the collector SUGGESTS it", func(t *testing.T) {
			// The stub declaration suggests summarizable=true and embeddable=true.
			// Neither may reach the entry: the two LLM axes are the operator's, and
			// a declaration that could set them would make installing a collector a
			// way to opt an operator into a bill.
			entry, _ := addedEntryBytes(t, nil)
			assert.Nil(t, behaviorBlock(t, entry)[flagPair.key],
				"the collector suggested this axis and the operator gave no flag; the entry must record no value")
		})

		t.Run(flagPair.key+"=false writes an explicit false", func(t *testing.T) {
			entry, _ := addedEntryBytes(t, []string{flagPair.flag + "=false"})
			got := behaviorBlock(t, entry)[flagPair.key]
			require.NotNil(t, got, "an explicit false must be WRITTEN, not dropped as a zero value")
			assert.JSONEq(t, "false", string(got))
		})

		t.Run(flagPair.key+"=true writes an explicit true", func(t *testing.T) {
			entry, _ := addedEntryBytes(t, []string{flagPair.flag + "=true"})
			got := behaviorBlock(t, entry)[flagPair.key]
			require.NotNil(t, got)
			assert.JSONEq(t, "true", string(got))
		})
	}
}

// TestCollectorAdd_FieldListFlags covers the list half's input classes: absent
// writes no key, given once, and repeated.
func TestCollectorAdd_FieldListFlags(t *testing.T) {
	t.Run("absent writes no key when the collector declares none either", func(t *testing.T) {
		entry, raw := addedEntryBytes(t, nil, withBareDeclaration())
		for _, key := range []string{"embed_fields", "summarize_fields", "bm25_fields"} {
			assert.NotContains(t, string(raw), key)
			assert.Nil(t, behaviorBlock(t, entry)[key])
		}
	})

	t.Run("absent takes the collector's DECLARED lists", func(t *testing.T) {
		// The field lists are facts about the collector's own nodes, and it is the
		// only thing that knows them; an operator who gave no flag gets what the
		// collector declared rather than the client's generic default.
		entry, _ := addedEntryBytes(t, nil)
		b := behaviorBlock(t, entry)
		for _, pair := range []struct{ key, path string }{
			{"embed_fields", "embed_fields"},
			{"summarize_fields", "summarize_fields"},
			{"bm25_fields", "bm25_fields"},
		} {
			want, err := json.Marshal(declaredStrings(t, "behavior", pair.path))
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(b[pair.key]),
				"the written %s must equal what the provider declared", pair.key)
		}
	})

	t.Run("a flag OVERRIDES the declared list", func(t *testing.T) {
		entry, _ := addedEntryBytes(t, []string{"--embed-fields", "keywords"})
		b := behaviorBlock(t, entry)
		assert.JSONEq(t, `["keywords"]`, string(b["embed_fields"]),
			"the operator's flag wins over the declaration for the same field")
		want, err := json.Marshal(declaredStrings(t, "behavior", "summarize_fields"))
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(b["summarize_fields"]),
			"and the lists the operator did not name still come from the declaration")
	})

	t.Run("given once and repeated", func(t *testing.T) {
		entry, _ := addedEntryBytes(t, []string{
			"--embed-fields", "summary", "--embed-fields", "content",
			"--summarize-fields", "content",
			"--bm25-fields", "symbol_name",
		})
		b := behaviorBlock(t, entry)
		assert.JSONEq(t, `["summary","content"]`, string(b["embed_fields"]),
			"a repeated flag appends in the order given — the composer joins in DECLARED order")
		assert.JSONEq(t, `["content"]`, string(b["summarize_fields"]))
		assert.JSONEq(t, `["symbol_name"]`, string(b["bm25_fields"]))
	})
}

// TestCollectorAdd_AnAddWithNoBehaviorOptionsWritesNoBehaviorKey is the control
// the per-flag rows above cannot supply.
//
// WHY THEY CANNOT. Those rows assert that a given flag's KEY is absent, and an
// empty behavior object satisfies that perfectly: `"behavior": {}` contains
// neither "summarizable" nor "embed_fields". So an implementation that wrote an
// empty block for every add would pass every row above while destroying the one
// property the block's absence carries — that the operator said nothing about
// this family's behavior at all, which is what the loader's defaults and the next
// reader both key on.
func TestCollectorAdd_AnAddWithNoBehaviorOptionsWritesNoBehaviorKey(t *testing.T) {
	// The provider declares the three booleans and NOTHING else — no field lists,
	// no overrides — so nothing but the operator could have put a behavior block
	// in this file.
	entry, raw := addedEntryBytes(t, nil, withBareDeclaration())
	assert.NotContains(t, entry, "behavior",
		"an add that mentioned no behavior option must write NO behavior key — an empty block "+
			"is a statement, and this operator made none: %s", raw)
	assert.NotContains(t, string(raw), "behavior",
		"and the key must be absent from the file bytes, not merely empty")
}

// TestCollectorAdd_TwoSpellingsPerOption follows the file's own convention: every
// option registers a short and a long name against the same variable.
func TestCollectorAdd_TwoSpellingsPerOption(t *testing.T) {
	long := []string{
		"--tool", "collect", "--summarizable=true", "--embeddable=false",
		"--embed-fields", "summary", "--summarize-fields", "content", "--bm25-fields", "keywords",
		"tickets", "--", "/p",
	}
	short := []string{
		"--tool", "collect", "--summarize=true", "--embed=false",
		"-ef", "summary", "-sf", "content", "-bf", "keywords",
		"tickets", "--", "/p",
	}
	fl, err := parseCollectorFlags("add", long, true)
	require.NoError(t, err)
	_, longEntry, err := entryFromArgs(fl)
	require.NoError(t, err)

	fs, err := parseCollectorFlags("add", short, true)
	require.NoError(t, err)
	_, shortEntry, err := entryFromArgs(fs)
	require.NoError(t, err)

	assert.Equal(t, longEntry, shortEntry, "the two spellings must write the same entry")
}

// TestCollectorAdd_ARefusedFieldListWritesNothing: a list value R2 refuses fails
// the add, and the file is untouched — the arc every other refusal in this
// command already follows.
func TestCollectorAdd_ARefusedFieldListWritesNothing(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)
	url := startContractProvider(t, true)
	var out bytes.Buffer
	err := runCollectorAdd([]string{
		"-t", "http", "--tool", contractStubTool,
		"--embed-fields", "summary", "--embed-fields", "summary",
		"tickets", url,
	}, &out)
	require.Error(t, err, "a duplicate field name is refused at load, so the add must fail")
	assert.Contains(t, err.Error(), "embed_fields")

	_, statErr := os.Stat(userPath)
	assert.True(t, os.IsNotExist(statErr),
		"a refused add writes NOTHING — an operator told the command failed must not find "+
			"a half-written entry")
}

// TestCollectorAdd_AFieldListFlagGivenAnEmptyValueFailsTheAdd is the fifth list
// input class, and the only one of the five that had no observation.
//
// WHY IT IS ITS OWN CLASS. A blank value is not a small list, it is a line the
// operator believed was doing work: `--embed-fields ""` names no field, resolves
// to nothing, and would compose text from a list one shorter than they wrote.
// listFlag.Set refuses it during the parse, which fails the add before the dial —
// so the file is never created, the same property the duplicate-value class
// already asserts.
//
// IT NEEDS NO PROVIDER. The refusal happens in flag parsing, before anything is
// dialed, so a case that stood one up would be testing the stub rather than the
// refusal.
func TestCollectorAdd_AFieldListFlagGivenAnEmptyValueFailsTheAdd(t *testing.T) {
	for _, flagName := range []string{
		"--embed-fields", "--summarize-fields", "--bm25-fields", "-ef", "-sf", "-bf",
	} {
		t.Run(flagName, func(t *testing.T) {
			userPath := useTempHome(t)
			useScratchCwd(t)

			var out bytes.Buffer
			err := runCollectorAdd([]string{
				"-t", "http", "--tool", contractStubTool, flagName, "", "tickets", "https://c.example/mcp",
			}, &out)
			require.Error(t, err, "a blank field name is not a field name; the add must fail")
			assert.Contains(t, err.Error(), "empty field name",
				"the refusal must say what is wrong")
			assert.Contains(t, err.Error(), flagLabelFor(flagName),
				"and it must NAME the flag, so an operator holding six list flags knows which one")

			_, statErr := os.Stat(userPath)
			assert.True(t, os.IsNotExist(statErr),
				"a refused add writes NOTHING — the same property the duplicate-value class asserts")
		})
	}
}

// flagLabelFor maps a spelling to the label its refusal names. Both spellings of
// one option share a single listFlag and therefore one label, which is the long
// form: an operator who typed the short spelling is told the option's name rather
// than the abbreviation they used.
func flagLabelFor(spelling string) string {
	switch spelling {
	case "--embed-fields", "-ef":
		return "--embed-fields"
	case "--summarize-fields", "-sf":
		return "--summarize-fields"
	default:
		return "--bm25-fields"
	}
}

// TestCollectorAdd_FiveValuesRoundTripToThePersistedRecord closes the loop R4's
// observable opens: what the flags write is what `collector get` prints and what
// reaches the server's behavior message with the same presence.
func TestCollectorAdd_FiveValuesRoundTripToThePersistedRecord(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)
	url := startContractProvider(t, true)
	var out bytes.Buffer
	require.NoError(t, runCollectorAdd([]string{
		"-t", "http", "--tool", contractStubTool,
		"--summarizable=true", "--embeddable=false",
		"--embed-fields", "summary", "--embed-fields", "content",
		"--summarize-fields", "content",
		"--bm25-fields", "symbol_name",
		"tickets", url,
	}, &out))

	loaded, found, err := collectorconfig.Loader{UserPath: userPath}.Resolve("tickets")
	require.NoError(t, err)
	require.True(t, found)
	b := loaded.Entry.Behavior
	require.NotNil(t, b)
	require.NotNil(t, b.Summarizable)
	require.NotNil(t, b.Embeddable)
	assert.True(t, *b.Summarizable)
	assert.False(t, *b.Embeddable)
	assert.Equal(t, []string{"summary", "content"}, b.EmbedFields)
	assert.Equal(t, []string{"content"}, b.SummarizeFields)
	assert.Equal(t, []string{"symbol_name"}, b.Bm25Fields)

	// The persisted record the server receives carries the same five with the same
	// presence — an explicit false stays an explicit false across the conversion.
	def := collectorconfig.Persisted("tickets", loaded.Entry).GetBehavior()
	require.NotNil(t, def.Summarizable)
	require.NotNil(t, def.Embeddable)
	assert.True(t, *def.Summarizable)
	assert.False(t, *def.Embeddable)
	assert.Equal(t, []string{"summary", "content"}, def.GetEmbedFields())
	assert.Equal(t, []string{"content"}, def.GetSummarizeFields())
	assert.Equal(t, []string{"symbol_name"}, def.GetBm25Fields())

	// `collector get` marshals the whole entry, so the five print for free once
	// they are written — which is R4's stated observable.
	var got bytes.Buffer
	require.NoError(t, runCollectorGet([]string{"tickets"}, &got))
	for _, want := range []string{
		`"summarizable": true`, `"embeddable": false`,
		`"embed_fields"`, `"summarize_fields"`, `"bm25_fields"`,
	} {
		assert.Contains(t, got.String(), want)
	}
}
