// SPDX-License-Identifier: Apache-2.0

package web

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mixedListNavHTML carries all three list shapes on ONE page, so the counting
// legs and the signals leg observe the same crawl rather than two arrangements
// that happen to agree.
//
// The three lists are, in order: a menu of bare anchors nested in <nav>, a menu
// of PROSE items declaring role="navigation", and a genuine three-bullet
// content list under <article>. Each is caught by a DIFFERENT rule of
// classifyList — the ancestor rule, the declared-role rule, and the per-item
// link-only measurement respectively — so no single rule can carry the fixture.
const mixedListNavHTML = `<!doctype html>
<html>
<head><title>Mixed Lists</title></head>
<body>
<nav><ul id="SiteMenu">
<li><a href="/one">Home</a></li>
<li><a href="/two">Downloads</a></li>
</ul></nav>
<ul role="navigation">
<li>Getting started with the collector</li>
<li>Configuring the crawl budget</li>
</ul>
<article>
<h1>Bulleted Prose</h1>
<ul>
<li>The walker classifies a list before it emits it.</li>
<li>The verdict travels with the measurements behind it.</li>
<li>A menu entry is a label rather than a chunk of prose.</li>
</ul>
</article>
</body>
</html>
`

// TestCollect_ListOnlyHarvest_IsSubstantiveButNavListStillFails drives the
// captured-only-chrome invariant through A REAL CRAWL in all three directions,
// and the trio is the point. It is the list-side counterpart of
// TestCollect_DataTableOnlyHarvest_IsSubstantiveButChromeStillFails.
//
// WHY IT HAD TO CHANGE. list_item was the one Content-filling type left out of
// the substantive sum, and the reason was honest: the collector had no verdict
// separating a menu entry from a bullet of prose, so counting list items would
// have silenced this leg for every nav-only harvest. A crawl of a site whose
// text is genuinely bulleted — release notes, requirement lists, checklists —
// was therefore refused outright as "captured only chrome" while its text sat
// in the graph.
//
// WHY WIDENING NEEDS THE OTHER TWO LEGS. A guard widened until nothing fails it
// is not a guard, and a subtraction applied to everything is the same defect
// wearing the opposite sign. The chrome control is a bare-anchor menu with NO
// <nav> ancestor and NO role, so only the per-item measurement can catch it.
// The mixed leg is the third input that neither extreme survives: an
// implementation subtracting EVERY list_item passes the prose leg's opposite
// and fails here, and so does one subtracting none.
func TestCollect_ListOnlyHarvest_IsSubstantiveButNavListStillFails(t *testing.T) {
	t.Run("a_prose_list_is_substantive_content", func(t *testing.T) {
		const proseListOnlyHTML = `<!doctype html>
<html>
<head><title>Release Notes</title></head>
<body><article>
<h1>Release Notes</h1>
<ul>
<li>The crawler now records why it judged a list to be navigation.</li>
<li>A description list measures its term and its body together.</li>
<li>Per-host politeness spaces request starts rather than serializing them.</li>
</ul>
</article></body>
</html>
`
		comp := serveComposition(t, "prose-list-only", proseListOnlyHTML)

		// THE PREMISE, asserted so the leg below cannot pass for the wrong
		// reason: this harvest really does carry its text ONLY in list items.
		assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node — that is what arms the page gate")
		assert.Positive(t, comp.NodesByType["list_item"], "the fixture must emit list_item nodes, or there is nothing to count")
		assert.Zero(t, comp.NodesByType["paragraph"],
			"the fixture must carry NO paragraph, or the invariant would pass on the old sum and this leg would prove nothing: %s", comp.Render())
		assert.Zero(t, comp.NodesByType["code_block"],
			"the fixture must carry NO code_block, for the same reason: %s", comp.Render())
		assert.Zero(t, comp.NodesByType["table"],
			"the fixture must carry NO table, for the same reason: %s", comp.Render())
		assert.Zero(t, comp.NonSubstantiveNodes,
			"a list of prose bullets is not retained chrome, so nothing here may be subtracted: %s", comp.Render())

		require.NoError(t, collector.CheckComposition("web", comp),
			"a harvest whose text lives in list items is a good harvest, not chrome: %s", comp.Render())
	})

	// THE CONTROL. A bare-anchor menu carries no <nav> ancestor and no role
	// here, deliberately: it is the shape of the real CWE menu, whose SiteMenu
	// and FooterMenu lists are bare anchors end to end with nothing declaring
	// what they are. The only rule that can catch it is the per-item link-only
	// measurement, so this leg fails any implementation that reads declarations
	// and never looks at the items.
	t.Run("a_nav_list_page_still_reads_as_chrome", func(t *testing.T) {
		const navListOnlyHTML = `<!doctype html>
<html>
<head><title>Navigation Only</title></head>
<body>
<ul id="SiteMenu">
<li><a href="/one">Home</a></li>
<li><a href="/two">Downloads</a></li>
<li><a href="/three">Community</a></li>
</ul>
</body>
</html>
`
		comp := serveComposition(t, "nav-list-only", navListOnlyHTML)

		assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node — that is what arms the page gate")

		// THE PREMISE: the items really are emitted, and really are counted as
		// retained chrome. BOTH COUNTS ARE PINNED EXACTLY rather than asserted
		// positive, because an exact count is what distinguishes "the classifier
		// counted these three" from "something else was counted" — a Positive()
		// assertion here is satisfied by any incidental chrome the page emits,
		// which is the very state this leg claims to reject.
		assert.Equal(t, 3, comp.NodesByType["list_item"],
			"the fixture emits exactly three list items: %s", comp.Render())
		assert.Equal(t, 3, comp.NonSubstantiveNodes,
			"all three items must be subtracted as nav-list chrome: %s", comp.Render())

		err := collector.CheckComposition("web", comp)
		require.Error(t, err, "a page whose only text is its menu is not a usable harvest: %s", comp.Render())
		assert.Contains(t, err.Error(), "harvest captured nothing usable")
	})

	// THE THIRD INPUT. Neither extreme survives a page holding both kinds: an
	// implementation subtracting every list_item reds on the no-error leg, and
	// one subtracting none reds on the count.
	t.Run("a_mixed_page_counts_only_the_genuine_items", func(t *testing.T) {
		comp := serveComposition(t, "mixed-lists", mixedListNavHTML)

		assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node — that is what arms the page gate")
		assert.Equal(t, 7, comp.NodesByType["list_item"],
			"two nav-nested items plus two role-declared items plus three prose bullets: %s", comp.Render())
		assert.Equal(t, 4, comp.NonSubstantiveNodes,
			"exactly the four menu entries are subtracted, and the three prose bullets are not: %s", comp.Render())

		require.NoError(t, collector.CheckComposition("web", comp),
			"three genuine bullets survive the subtraction, so this harvest is usable: %s", comp.Render())
	})

	// THE VERDICT CARRIES ITS INPUTS. Each of the three lists is judged by a
	// DIFFERENT rule, and each leg below reads a measurement that the other two
	// rules could not have produced — the role-declared menu's link-only count
	// of ZERO is what proves its verdict was not reached by counting links.
	t.Run("every_list_carries_the_measurements_behind_its_verdict", func(t *testing.T) {
		_, batch := serveCrawl(t, "mixed-list-signals", mixedListNavHTML)

		lists := nodesWhere(batch.Nodes, func(n *knowledgev1.Node) bool { return n.Type == "list" })
		require.Len(t, lists, 3,
			"all three lists must reach the graph — a navigation verdict is a signal on the record, never a reason to omit it")

		var navNested, roleDeclared, contentList *knowledgev1.Node
		for _, l := range lists {
			switch {
			case l.Metadata["list_ancestry"] == "nav":
				navNested = l
			case l.Metadata["list_role"] == "navigation":
				roleDeclared = l
			default:
				contentList = l
			}
		}
		require.NotNil(t, navNested, "the <nav>-nested menu must report its sectioning ancestor")
		require.NotNil(t, roleDeclared, "the role-declaring menu must report its declared role")
		require.NotNil(t, contentList, "the content list must reach the graph")

		for _, l := range lists {
			assert.Contains(t, l.Metadata, "list_nav",
				"every list carries its verdict for BOTH answers, or an absent key is ambiguous with an older graph: %v", l.Metadata)
			assert.Contains(t, l.Metadata, "list_item_count", "every list carries the count it measured: %v", l.Metadata)
			assert.Contains(t, l.Metadata, "list_link_only_items", "every list carries the count it measured: %v", l.Metadata)
		}

		assert.Equal(t, "true", navNested.Metadata["list_nav"], "a list inside <nav> is navigation")
		assert.Equal(t, "nav", navNested.Metadata["list_ancestry"])
		assert.Equal(t, "2", navNested.Metadata["list_item_count"])
		assert.Equal(t, "2", navNested.Metadata["list_link_only_items"],
			"both of its entries are bare anchors")

		assert.Equal(t, "true", roleDeclared.Metadata["list_nav"], "role=navigation is an author declaration")
		assert.Equal(t, "navigation", roleDeclared.Metadata["list_role"])
		assert.Equal(t, "2", roleDeclared.Metadata["list_item_count"])
		assert.Equal(t, "0", roleDeclared.Metadata["list_link_only_items"],
			"NONE of its entries is link-only, which is what proves this verdict was reached by the declaration and not by counting links")

		assert.Equal(t, "false", contentList.Metadata["list_nav"], "a list of prose bullets is content")
		assert.Equal(t, "3", contentList.Metadata["list_item_count"])
		assert.Equal(t, "0", contentList.Metadata["list_link_only_items"])
		assert.NotContains(t, contentList.Metadata, "list_role",
			"no role attribute is an ABSENCE, not a role of \"\": %v", contentList.Metadata)
		assert.NotContains(t, contentList.Metadata, "list_ancestry",
			"no sectioning ancestor is an ABSENCE, not an ancestry of \"\": %v", contentList.Metadata)

		// EVERY ITEM INHERITS ITS OWN LIST'S VERDICT, walked over the contains
		// edges rather than over a count, so an implementation stamping the
		// right NUMBER of items on the wrong lists still reds here.
		byID := map[string]*knowledgev1.Node{}
		for _, n := range batch.Nodes {
			byID[n.Id] = n
		}
		itemsByList := map[string][]*knowledgev1.Node{}
		for _, e := range batch.Edges {
			if e.Type != kgtypes.EdgeContains {
				continue
			}
			// The type is the LITERAL "list_item" rather than the
			// webListItemType constant deliberately: this file must compile
			// against a tree that does not yet declare that constant, so its
			// red direction is behavioral rather than an absent-symbol build
			// failure.
			child, ok := byID[e.ToID]
			if !ok || child.Type != "list_item" {
				continue
			}
			itemsByList[e.FromID] = append(itemsByList[e.FromID], child)
		}

		linkOnlyItems := 0
		for _, l := range lists {
			items := itemsByList[l.Id]
			require.NotEmpty(t, items, "list id=%s reached the graph with no items under it", l.Id)
			for _, item := range items {
				assert.Equal(t, l.Metadata["list_nav"], item.Metadata["list_nav"],
					"item id=%s must inherit its list's verdict", item.Id)
				if item.Metadata["list_item_link_only"] == "true" {
					linkOnlyItems++
				}
				assert.NotEmpty(t, item.Content,
					"item id=%s must keep its text in Content — the verdict is a signal, never a relocation of the text", item.Id)
			}
		}
		assert.Equal(t, 2, linkOnlyItems,
			"exactly the two bare-anchor entries are link-only; the role-declared menu's prose entries are not")
	})
}
