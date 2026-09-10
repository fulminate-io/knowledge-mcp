// SPDX-License-Identifier: Apache-2.0

// Package tools — graph_names.go holds the client-side graph-catalog
// enumeration helper the cross-graph locator, repo resolver, and the
// per-account resource overview share. It reduces the bespoke
// pipeline_list_graphs RPC to the generic Execute seam
// (RETURN_MODE_GRAPH_NAMES), mirroring linker.fetchGraphNames but living in
// package tools and returning the full []*knowledgev1.GraphInfo (callers
// project Name themselves; the catalog's size projection and the manage(index)
// branch table need the full FilePath/FileSize/Nodes/Edges/Loaded fields).

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// fetchGraphNamesOfType enumerates every loaded graph of graphType via the
// generic Execute seam: a query(mode:modules) compiled to RETURN_MODE_GRAPH_NAMES
// whose graph_names_json carrier decodes to []*knowledgev1.GraphInfo. It replaces
// the bespoke pipeline_list_graphs RPC (the count-free ListGraphsLite read the
// server runs is the same one the modules query lowers to). One Execute per call,
// no fan-out.
//
// Returns the FULL []*knowledgev1.GraphInfo (NOT []string) so callers project Name
// themselves and a caller rendering per-graph size and load state can reuse the
// FilePath/FileSize/Nodes/Edges/Loaded fields. A missing-seam / decode failure
// surfaces as an error so the OBJECT-shape mismatch is loud (mirroring the linker
// helper's non-masking contract).
//
// overlayOf is variadic and source-compatible with the base-only callers: when
// overlayOf[0] is set, it threads onto the QueryPlan as overlay_of so
// ListGraphsLite restricts the enumeration to the overlay keys of that base
// ("<type>/<overlayOf[0]>@*") instead of the base names. Empty/absent = base-name
// enumeration (today's behavior).
func fetchGraphNamesOfType(ctx context.Context, gc GraphCaller, graphType string, overlayOf ...string) ([]*knowledgev1.GraphInfo, error) {
	if gc == nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType: graph caller unavailable")
	}
	argMap := map[string]any{
		"graph": graphType,
		"mode":  "modules",
	}
	if len(overlayOf) > 0 && overlayOf[0] != "" {
		argMap["overlay_of"] = overlayOf[0]
	}
	args, err := json.Marshal(argMap)
	if err != nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType(%s): marshal: %w", graphType, err)
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType(%s): %w", graphType, err)
	}
	infos, err := engine.DecodeGraphNames(resp)
	if err != nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType(%s): decode: %w", graphType, err)
	}
	return infos, nil
}

// catalogEntry is one enumerated graph reduced to the two facts the catalog read
// can supply WITHOUT touching the graph: its instance name and its image's size on
// disk.
//
// BOTH ARE COLD BY CONSTRUCTION on the server side of this call. The enumeration
// lowers to store.ListGraphsLite -> Registry.listGraphs, which os.ReadDirs the
// type's directory and takes the size off each DirEntry's Info; it fills node and
// edge counts ONLY for graphs already resident, and never loads one to answer. That
// is what makes the size safe to render for a graph the coverage walk is otherwise
// forbidden to read (manage_status_coverage_unmanaged.go).
type catalogEntry struct {
	name       string
	imageBytes int64
}

// listCatalogOfType is listGraphNamesOfType's WIDER PROJECTION of the SAME single
// enumeration RPC — name plus on-disk size, off one call rather than two.
//
// It is a sibling rather than a replacement because the name-only helper has a
// dozen callers that want exactly a []string, and widening those would spread a
// carrier through call sites with no use for it. Empty names are dropped here for
// the identical reason they are dropped there: the default knowledge graph
// enumerates an empty instance name and is emitted explicitly by its own caller.
func listCatalogOfType(ctx context.Context, deps ClientDeps, graphType string) ([]catalogEntry, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("graph client unavailable")
	}
	infos, err := fetchGraphNamesOfType(ctx, gc, graphType)
	if err != nil {
		return nil, err
	}
	entries := make([]catalogEntry, 0, len(infos))
	for _, gi := range infos {
		if gi.GetName() == "" {
			continue
		}
		entries = append(entries, catalogEntry{name: gi.GetName(), imageBytes: gi.GetFileSize()})
	}
	return entries, nil
}

// catalogNames projects the entries back to the bare name list the callers that
// only need identities take.
func catalogNames(entries []catalogEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
	}
	return names
}

// listGraphNamesOfType enumerates the loaded graph names of the given type via
// the generic Execute seam (RETURN_MODE_GRAPH_NAMES, the fetchGraphNamesOfType
// helper) — the client graph-overview source. Empty names are dropped.
func listGraphNamesOfType(ctx context.Context, deps ClientDeps, graphType string) ([]string, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("graph client unavailable")
	}
	infos, err := fetchGraphNamesOfType(ctx, gc, graphType)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, gi := range infos {
		if gi.Name != "" {
			names = append(names, gi.Name)
		}
	}
	return names, nil
}

// ListGraphNamesOfType is the exported cross-package seam over listGraphNamesOfType
// (the same RETURN_MODE_GRAPH_NAMES enumeration the status coverage table uses):
// the bootstrap segment-coverage reconcile enumerates code repos through it so it
// probes exactly the graph set manage(status) reports. Empty names are dropped.
func ListGraphNamesOfType(ctx context.Context, deps ClientDeps, graphType string) ([]string, error) {
	return listGraphNamesOfType(ctx, deps, graphType)
}

// listOverlayKeysOfBase enumerates the OVERLAY keys of a single base graph via
// the same Execute seam (RETURN_MODE_GRAPH_NAMES) with overlay_of set to the base.
//
// THE RETURNED NAME FORM IS BACKEND-DEPENDENT: the returned GraphInfo.Name values
// are the FULL "base@overlay" key on the CLOUD backend and the BARE overlay name on the OSS/local backend, whose registry_lookup.go listOverlays sets gi.Name to the overlay with the base prefix already stripped.
// CALLERS MUST THEREFORE NORMALIZE WITH bareOverlayName rather than assuming
// either form — assuming the composed one drops every OSS overlay, assuming the
// bare one composes a doubled base prefix.
//
// What holds on BOTH backends: this returns ONLY the overlay keys — NOT the base
// graph itself and NOT every graph (the base name comes from the separate
// listGraphNamesOfType enumeration) — and empty names are dropped. It is a
// distinct call shape from listGraphNamesOfType (which returns base names and
// filters @-keys), hence a sibling rather than an extension.
func listOverlayKeysOfBase(ctx context.Context, deps ClientDeps, graphType, base string) ([]string, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("graph client unavailable")
	}
	infos, err := fetchGraphNamesOfType(ctx, gc, graphType, base)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, gi := range infos {
		if gi.Name != "" {
			keys = append(keys, gi.Name)
		}
	}
	return keys, nil
}
