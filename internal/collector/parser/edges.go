// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// resolveEdges resolves raw edge IDs (pkg.Symbol or pkg.Receiver.Method format)
// to actual node IDs (filepath:Symbol format used by the graph). Uses an in-memory
// symbol map for same-build resolution.
//
// Edges where both FromID and ToID can be resolved are included in the result.
// Unresolvable edges (e.g., calls to stdlib) are dropped.
// IMPORTS edges are passed through unchanged.
func resolveEdges(edges []*knowledgev1.Edge, symbolMap map[string]string, nodeIDs map[string]bool) []*knowledgev1.Edge {
	// Build reverse lookup: language partition + bare symbol name → []nodeID for
	// cross-package resolution. The partition keeps a Go caller's candidate set
	// free of same-named symbols from other languages, whose namespaces carry a
	// "<language>:" prefix; Go namespaces are bare package names and land in the
	// "" partition. The NUL separator is used because no namespace or symbol
	// contains one.
	byName := make(map[string][]string)
	// Build pkg:method → []nodeID for same-package method resolution when
	// the symbolMap key includes a receiver type (e.g., "store.db.Retrieve").
	// This key already begins with the full namespace token, so it is partitioned
	// across languages by construction.
	byPkgName := make(map[string][]string)
	for qualName, nodeID := range symbolMap {
		parts := strings.Split(qualName, ".")
		if len(parts) >= 2 {
			method := parts[len(parts)-1]
			pkg := parts[0]
			byName[nsLang(pkg)+"\x00"+method] = append(byName[nsLang(pkg)+"\x00"+method], nodeID)
			byPkgName[pkg+":"+method] = append(byPkgName[pkg+":"+method], nodeID)
		}
	}

	var resolved []*knowledgev1.Edge
	for _, edge := range edges {
		// IMPORTS edges use file path → import path; no resolution needed.
		// The edges are freshly built by chunkResultsToPopulate / ConvertEdges
		// (not shared), so the pointer is appended directly.
		if kgtypes.EdgeType(edge.Type) == kgtypes.EdgeImports {
			resolved = append(resolved, edge)
			continue
		}

		origFrom := edge.FromId

		// Resolve FromID.
		newFrom := resolveEdgeID(edge.FromId, "", "", symbolMap, nodeIDs, byName, byPkgName)
		if newFrom == "" {
			continue
		}

		// Extract caller's package and receiver from original qualified name.
		// Format: "pkg.Func" or "pkg.Receiver.Method"
		callerPkg, callerReceiver := extractCallerContext(origFrom)

		// Resolve ToID.
		newTo := resolveEdgeID(edge.ToId, callerPkg, callerReceiver, symbolMap, nodeIDs, byName, byPkgName)
		if newTo == "" {
			continue
		}

		edge.FromId = newFrom
		edge.ToId = newTo
		resolved = append(resolved, edge)
	}
	return resolved
}

// nsLang returns the language partition of a namespace token: the prefix
// before the ':' for non-Go namespaces, "" for Go package names.
func nsLang(ns string) string {
	if before, _, ok := strings.Cut(ns, ":"); ok {
		return before
	}
	return ""
}

// extractCallerContext extracts the namespace and receiver type from a
// qualified edge ID. For "store.db.Retrieve" returns ("store", "db").
// For "store.Open" returns ("store", "").
func extractCallerContext(qualName string) (pkg, receiver string) {
	parts := strings.Split(qualName, ".")
	if len(parts) >= 2 {
		pkg = parts[0]
	}
	if len(parts) >= 3 {
		receiver = parts[1]
	}
	return
}

// resolveEdgeID attempts to resolve a raw ID to a graph node ID.
//
// Resolution order:
//  1. Direct qualified name lookup in symbolMap (e.g., "store.db.Retrieve")
//  2. Already a known node ID (e.g., file path for CONTAINS edges)
//  3. Same-package resolution:
//     a. Receiver-qualified: callerPkg.callerReceiver.id (e.g., "store.db.Retrieve")
//     b. Direct: callerPkg.id (e.g., "store.Open")
//     c. Fuzzy: any same-package symbol with this name (unambiguous only)
//  4. Unambiguous cross-package lookup within the id's language partition (bare
//     name found in exactly one package of that language)
//  5. For dotted IDs (e.g., "g.Close" from variable method calls): extract the last
//     segment and retry steps 3-4 with just the method name
//
// Returns "" if unresolvable.
func resolveEdgeID(id, callerPkg, callerReceiver string, symbolMap map[string]string, nodeIDs map[string]bool, byName, byPkgName map[string][]string) string {
	// 1. Direct qualified name lookup.
	if nodeID, ok := symbolMap[id]; ok {
		return nodeID
	}

	// 2. Already a valid node ID (file paths for CONTAINS FromID, etc.).
	if nodeIDs[id] {
		return id
	}

	// 3a. Same-package receiver-qualified (caller and callee share receiver type).
	if callerPkg != "" && callerReceiver != "" {
		if nodeID, ok := symbolMap[callerPkg+"."+callerReceiver+"."+id]; ok {
			return nodeID
		}
	}
	// 3b. Same-package direct (standalone function in same package).
	if callerPkg != "" {
		if nodeID, ok := symbolMap[callerPkg+"."+id]; ok {
			return nodeID
		}
		// 3c. Fuzzy same-package: any receiver type with this method (unambiguous only).
		if candidates := byPkgName[callerPkg+":"+id]; len(candidates) == 1 {
			return candidates[0]
		}
	}

	// The bare-name partition comes from the id being resolved when the id
	// carries its own namespace, and from the caller otherwise. The inner
	// guard matters: a dotted callee that is not a namespace — TypeScript
	// captures a whole member_expression, so `this.area()` arrives as
	// "this.area" — must keep the caller's partition rather than being
	// reassigned to Go's.
	part := nsLang(callerPkg)
	if before, _, ok := strings.Cut(id, "."); ok {
		if l := nsLang(before); l != "" {
			part = l
		}
	}

	// 4. Unambiguous cross-package resolution within the language partition.
	if candidates := byName[part+"\x00"+id]; len(candidates) == 1 {
		return candidates[0]
	}

	// 5. For dotted IDs like "g.Close" (method calls on variables), extract the
	// last segment and retry resolution with just the method name.
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		if nodeID := resolveDottedEdgeCallee(id[idx+1:], callerPkg, callerReceiver, part, symbolMap, byName, byPkgName); nodeID != "" {
			return nodeID
		}
	}

	return ""
}

// resolveDottedEdgeCallee resolves a method name extracted from a dotted ID
// (e.g., "Close" from "g.Close") by retrying same-package and cross-package lookups.
// part is the language partition resolveEdgeID derived from the full dotted id;
// methodName is the last segment and so never carries a namespace of its own.
func resolveDottedEdgeCallee(methodName, callerPkg, callerReceiver, part string, symbolMap map[string]string, byName, byPkgName map[string][]string) string {
	// Try receiver-qualified (same receiver calling own method).
	if callerPkg != "" && callerReceiver != "" {
		if nodeID, ok := symbolMap[callerPkg+"."+callerReceiver+"."+methodName]; ok {
			return nodeID
		}
	}
	if callerPkg != "" {
		// Try direct same-package.
		if nodeID, ok := symbolMap[callerPkg+"."+methodName]; ok {
			return nodeID
		}
		// Try fuzzy same-package (unambiguous only).
		if candidates := byPkgName[callerPkg+":"+methodName]; len(candidates) == 1 {
			return candidates[0]
		}
	}
	if candidates := byName[part+"\x00"+methodName]; len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}
