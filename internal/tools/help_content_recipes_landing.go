// SPDX-License-Identifier: Apache-2.0

package tools

// help_content_recipes_landing.go carries the LANDING half of help("recipes"),
// split out of help_content_recipes.go for the same reason its two existing
// siblings were: that file sits against the repo's 500-line per-file ceiling.
// helpRecipes composes this constant onto the end of its body.

const helpRecipesLanding = "\n" +
	"## Landing — writing what a recipe emits into the practice graph\n" +
	"\n" +
	"`land: true` writes the emitted nodes into the COMBINED PRACTICE\n" +
	"graph. They land as BASIC nodes: the recipe produces a starting\n" +
	"point, and a human massages it into golden state afterwards. A\n" +
	"recipe never produces curated content.\n" +
	"\n" +
	"    collect({ type: \"web\", id: \"hohpe-eip\", transformer: \"recipe\",\n" +
	"              land: true, recipe_body: \"select section\\nemit ...\" })\n" +
	"\n" +
	"### What a landing writes\n" +
	"\n" +
	"- Every emitted node, carrying the emit body's `type`, `name`,\n" +
	"  `summary`, `description`, `content`, `status` and any other field\n" +
	"  as metadata. A node of a known practice type with NO `summary` is\n" +
	"  refused by the server — supply one in the emit body.\n" +
	"- A `source` HUB node named from the raw graph's slug, created on the\n" +
	"  first landing and reused after. It carries a `kind` marker (`web`\n" +
	"  or `pdf`) and the raw graph's own recorded origin, so it still says\n" +
	"  where the collection came from after the raw graph is dropped.\n" +
	"- One `sourced-from` edge per node, pointing node → hub, plus the\n" +
	"  hub's id in each node's `source_hub` metadata. The edge is what a\n" +
	"  traverse walks; the metadata key is what a browse, a search and the\n" +
	"  by-hub delete narrow on.\n" +
	"- NOTHING pointing back into the raw graph. Raw graphs are scratch\n" +
	"  that you drop once the curated set exists, so an edge into one\n" +
	"  would point at rows that stop existing.\n" +
	"\n" +
	"The node's own `source` field keeps the emitter's `recipe:<slug>`\n" +
	"stamp and is NOT the hub — the two are different facts.\n" +
	"\n" +
	"### Re-running a landing: versioned twins\n" +
	"\n" +
	"An emitted id is deterministic, so a second landing over the same\n" +
	"document produces the same ids. Where one ALREADY EXISTS the landing\n" +
	"does not touch it: it writes a VERSIONED TWIN — a new node with a\n" +
	"distinct id, an automatic integer `version` in its metadata, and one\n" +
	"`next-version` edge from the old row to the new one. Both are kept.\n" +
	"\n" +
	"That is what makes a landing safe over a hub whose nodes a human has\n" +
	"since edited: a hand-edited row is a resident like any other, so it\n" +
	"gets a twin rather than an overwrite. There is no flag that changes\n" +
	"this — `force` is refused precisely because nothing is overwritten.\n" +
	"\n" +
	"### What a landing refuses\n" +
	"\n" +
	"- `max_rows` and `offset`. They window what an extract SHOWS, and a\n" +
	"  landing writes the whole emitted set; accepting them would write\n" +
	"  more than the response reports. Page with an extract instead.\n" +
	"- A combined practice graph that does not exist yet. It is created by\n" +
	"  the first write to it; author one practice node by hand and land\n" +
	"  again.\n" +
	"- A raw graph whose root records no source (`path` for pdf,\n" +
	"  `seed_host` for web). That is a graph collected before its family\n" +
	"  began stamping one; re-collect the document and land again.\n" +
	"- A resident read that came back truncated, because a landing that\n" +
	"  did not see every resident would overwrite the ones it missed.\n" +
	"\n" +
	"### Removing a landed collection\n" +
	"\n" +
	"    delete({ graph: \"practice\", source: \"<hub id>\", dry_run: true })\n" +
	"\n" +
	"deletes every node under that hub, and the hub with them. It is SOFT\n" +
	"by default (tombstoned, recoverable); `hard: true` sweeps the rows and\n" +
	"their edges permanently. Run it with `dry_run: true` first — the\n" +
	"preview resolves the identical set the delete would. Nodes under any\n" +
	"other hub are untouched. On `mutate` the same axis is spelled\n" +
	"`source_hub`, because `source` there is the node's own provenance.\n" +
	"\n" +
	"### The reading loop, end to end\n" +
	"\n" +
	"1. `collect` the document into a raw graph.\n" +
	"2. Iterate with `extract: true` until the body says what you meant.\n" +
	"3. `land: true` once.\n" +
	"4. Massage the landed nodes by hand into golden state.\n" +
	"5. Drop the raw graph — it is scratch.\n" +
	""
