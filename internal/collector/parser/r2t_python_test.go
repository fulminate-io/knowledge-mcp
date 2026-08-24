// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestR2TBindsPython proves the python qualifier and type-facts arms bind END TO
// END through the real chunker, declaration index and resolution ladder.
func TestR2TBindsPython(t *testing.T) {
	t.Run("self_receiver_field_hop", func(t *testing.T) {
		// TWO ROUTES IN ONE ASSERTION, and they are genuinely chained rather than
		// merely both present: `self.store.get()` splits into the base `self` and
		// the field `store`, so the receiver route has to resolve `self` to Server
		// before the field hop can read Server's annotated attribute for `store`.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "bin/svc.py", src: "class Store:\n    def get(self):\n        pass\n\n\n" +
				"class Server:\n    store: Store\n\n    def run(self):\n        self.store.get()\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "bin/svc.py:Server.run", "bin/svc.py:Store.get")
	})

	t.Run("typed_parameter", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "bin/param.py", src: "class Config:\n    def load(self):\n        pass\n\n\n" +
				"def run(cfg: Config):\n    cfg.load()\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "bin/param.py:run", "bin/param.py:Config.load")
	})

	t.Run("imported_type_binds", func(t *testing.T) {
		// THE CROSS-FILE DIRECT-TYPE ROUTE FOR PYTHON. The binds arm records
		// `from bin.conf import Config` under the LOCAL name with the declaring
		// module's scope, which is exactly what the index-aware type-text helper
		// reads — so an annotation naming an imported class resolves to the other
		// module's declaration.
		//
		// THE SPECIFIER IS PACKAGE-QUALIFIED because the binds arm resolves a
		// python module path from the REPOSITORY ROOT — `bin.conf` becomes
		// bin/conf.py — rather than relative to the importing file's directory.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "bin/conf.py", src: "class Config:\n    def load(self):\n        pass\n"},
			{path: "bin/app.py", src: "from bin.conf import Config\n\n\ndef run(cfg: Config):\n    cfg.load()\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "bin/app.py:run", "bin/conf.py:Config.load")

		// KNOWN-NEGATIVE CONTROL: the importing module declares no Config of its
		// own, so the edge above cannot be a same-file hit wearing a cross-file
		// label.
		requireNoCallTo(t, res, "bin/app.py:run", "bin/app.py:Config.load")
	})

	t.Run("unannotated_emits_no_typed_qualifier", func(t *testing.T) {
		// THE BIND-ONLY NO-REGRESSION GUARD. The same fixtures with every
		// annotation removed must behave exactly as they did before this ticket
		// existed: the rung binds nothing, so no edge is attributed to it.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "bin/plain.py", src: "class Store:\n    def get(self):\n        pass\n\n\n" +
				"class Server:\n    store = None\n\n    def run(self):\n        self.store.get()\n"},
			{path: "bin/plainparam.py", src: "class Config:\n    def load(self):\n        pass\n\n\n" +
				"def run(cfg):\n    cfg.load()\n"},
		}, nil)

		var typed []string
		for _, e := range res.Edges {
			if e.Method == string(RuleTypedQualifier) {
				typed = append(typed, e.FromId+" -> "+e.ToId)
			}
		}
		assert.Empty(t, typed, "an unannotated python file must produce no typed-qualifier edge")

		// KNOWN-POSITIVE CONTROL, and it is the whole reason this subtest is not
		// vacuous: the same populate pass over the SAME shapes WITH annotations
		// does produce such an edge, so the emptiness above is the absence of
		// annotations rather than the rung being switched off, the arm being
		// unregistered, or the fixture producing no edges at all.
		require.NotEmpty(t, res.Edges, "control: the unannotated fixture produced edges of some kind")
		annotated := populateRepoFixture(t, []fixtureFile{
			{path: "bin/annotated.py", src: "class Config:\n    def load(self):\n        pass\n\n\n" +
				"def run(cfg: Config):\n    cfg.load()\n"},
		}, nil)
		var sawTyped bool
		for _, e := range annotated.Edges {
			if e.Type == string(kgtypes.EdgeCalls) && e.Method == string(RuleTypedQualifier) {
				sawTyped = true
				break
			}
		}
		require.True(t, sawTyped,
			"control: the annotated form of the same shape DOES bind through the typed-qualifier rung")
	})
}
