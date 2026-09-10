// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// context.go — the DECLARED FOREIGN-GRAPH CONTEXT: what a registration declares
// it needs from other graph families, and the block the client fills from that
// declaration and sends on the collect input.
//
// IT IS DECLARED-ONLY WITH NO BASELINE, exactly as the environment allowlist is
// (mcphost.go's doc comment states the rule this copies). A collector receives
// what its entry declared and nothing else: no family it did not name, no field
// it did not list, and no block at all when it declared none. The consequence
// that matters is the one an operator can check: what a module can compute is a
// function of what its entry says, readable in the file, and never of what the
// daemon happened to have loaded.
//
// THE DECLARATION AND THE BLOCK LIVE IN THE SAME PACKAGE AS THE SCHEMA THEY
// DESCRIBE, and the reason is the one-source rule contract.go states for the
// contract itself: the family names and the field names are contract vocabulary,
// and a second copy beside the checked-in schema is two sources that drift. The
// config package that reads the operator's file references these types rather
// than restating them, which is also what lets a Registration carry the
// declaration into the registration-time check.
//
// THE FAMILY IS A MAP KEY RATHER THAN A STRUCT FIELD, on the DECLARATION AND ON
// THE BLOCK ALIKE. A struct with a field per family would make an unrecognized
// family a strict-decode failure in the config loader, which reads to an
// operator as a malformed file; a map admits the key and lets a validator refuse
// it BY NAME, on the fill path as well as at load. The block is a map for a
// second reason that is now the stronger one: THE SUPPLYABLE FAMILY SET IS NOT
// KNOWN AT COMPILE TIME. It is `code` plus whatever graph types are registered
// on this daemon, which is an operator-time fact, and a closed struct cannot
// carry a key set the client learns from the registry at run time.
//
// WHICH FAMILY NAMES ARE LEGAL IS THEREFORE NOT DECIDED HERE. This package is
// contract vocabulary and cannot reach the graph-type registry, so Validate
// checks everything that IS closed — the node fields, the edge fields, the
// coherence of a declaration — and the family names are checked by
// ValidateFamilies, which the fill path calls with the set it read from the
// registry. Splitting the two is what keeps the config loader able to refuse a
// malformed entry at write time without a running server.

// contextInputProperty is the collect input's top-level property the block
// travels on. It is the one spelling three places must agree on — the
// checked-in schema, the argument composition and the framework's own decode
// target — so it is named once here.
const contextInputProperty = "context"

// ContextFamilyCode is the code graph family: the indexed repositories' names
// and, where declared, their nodes. It is the ONE family named as a constant,
// because it is the one the contract itself reasons about: path_basenames
// narrows file paths and applies to it alone. Every other supplyable family is a
// REGISTERED GRAPH TYPE, named by the operator's registration rather than by
// this package, and so has no constant to be named by.
const ContextFamilyCode = "code"

// The node fields a declaration may name. They are the contract's own output
// vocabulary (envelope.go), so a collector author reads one set of spellings on
// both sides of the wire.
const (
	ContextNodeFieldID         = "id"
	ContextNodeFieldType       = "type"
	ContextNodeFieldSymbolName = "symbol_name"
	ContextNodeFieldFilePath   = "file_path"
	ContextNodeFieldContent    = "content"
)

// The edge fields a declaration may name. Only the two endpoints are supplyable:
// they are what a graph's edges carry that a module can compute from, and a
// field nothing fills is refused rather than sent empty.
const (
	ContextEdgeFieldFromID = "from_id"
	ContextEdgeFieldToID   = "to_id"
)

// contextNodeFields and contextEdgeFields are the supplyable sets Validate
// checks against. They are sorted so a refusal lists them in a stable order.
var (
	contextNodeFields = []string{
		ContextNodeFieldContent,
		ContextNodeFieldFilePath,
		ContextNodeFieldID,
		ContextNodeFieldSymbolName,
		ContextNodeFieldType,
	}
	contextEdgeFields = []string{
		ContextEdgeFieldFromID,
		ContextEdgeFieldToID,
	}
)

// ContextDeclaration is what one registration declares it needs from other graph
// families, keyed by family. An absent or empty declaration means the collector
// receives no context block at all.
type ContextDeclaration map[string]FamilyDeclaration

// FamilyDeclaration is one family's declared slice: which node types enter it,
// which fields of those nodes are carried, which metadata keys are carried, and
// which edge fields are carried.
//
// AN EMPTY FAMILY YIELDS ENTRIES CARRYING THE GRAPH NAME AND NO NODES, which is
// a real declaration rather than a degenerate one: a module whose predicate is a
// membership test over graph names needs exactly that and nothing more. Asking
// for node fields or metadata keys WITHOUT node types is a different thing and is
// refused — see validate.
//
// EVERY NODE OF THE FAMILY IS A THIRD DECLARATION, spelled all_node_types. It is
// its own field precisely so the empty form above keeps its meaning; see
// AllNodeTypes.
type FamilyDeclaration struct {
	// NodeTypes selects which node types enter the slice at all. Empty means no
	// nodes — the graph names alone.
	NodeTypes []string `json:"node_types,omitempty"`
	// AllNodeTypes selects EVERY node of the family, whatever its type.
	//
	// IT IS ITS OWN FIELD RATHER THAN A REDEFINITION OF THE EMPTY LIST, and that
	// is the whole design. An empty NodeTypes already MEANS something — the graph
	// names and no nodes, which is exactly what a membership test over graph
	// names needs — so reading it as "every type" would silently turn every such
	// declaration into a full drain of every graph of the family.
	//
	// WHAT IT IS FOR: a family whose type vocabulary is open or too large to
	// name. A cloud provider emits one node type per resource kind — forty-nine
	// of them for one provider in this tree — and a declaration could otherwise
	// select them only by naming each exactly, which is a list that rots the day
	// the provider adds a resource.
	//
	// SETTING IT BESIDE A NON-EMPTY NodeTypes IS REFUSED BY NAME: the two say
	// different things, and silently preferring one is the ambiguity this
	// spelling exists to remove.
	AllNodeTypes bool `json:"all_node_types,omitempty"`
	// NodeFields names the typed node fields carried on each node.
	NodeFields []string `json:"node_fields,omitempty"`
	// MetadataKeys names the metadata keys carried on each node. A key the node
	// does not hold is OMITTED rather than carried empty, on the same rule the
	// environment allowlist follows: an absent value and an empty one are
	// different inputs.
	MetadataKeys []string `json:"metadata_keys,omitempty"`
	// EdgeFields names the edge fields carried on each edge. Empty means no
	// edges.
	EdgeFields []string `json:"edge_fields,omitempty"`
	// PathBasenames narrows the code family's nodes to those whose file path has
	// one of these basenames.
	//
	// IT IS A DECLARATION FACILITY AND NEVER A SIZE CAP. An entry is free to
	// declare none and receive every node of the declared types, and what is
	// declared is sent whole. Declaring less is a statement about what the module
	// needs, not a limit imposed on it.
	PathBasenames []string `json:"path_basenames,omitempty"`
}

// IsEmpty reports whether this declaration asks for nothing at all.
func (d ContextDeclaration) IsEmpty() bool { return len(d) == 0 }

// Validate refuses a declaration whose CLOSED vocabulary is wrong, naming the
// family and the value that is wrong with it. It checks the node fields, the
// edge fields, the coherence of each family's slice and the one family-scoped
// narrowing — everything whose legal set is fixed at compile time.
//
// IT DOES NOT CHECK THE FAMILY NAMES, and that is a split rather than an
// omission: which families can be supplied is `code` plus the graph types
// registered on the daemon, which this package cannot read. ValidateFamilies is
// that half, called by the fill path with the registry's answer. The config
// loader calls this one alone, so a hand-written entry is still refused at write
// time on every ground that does not need a running server.
//
// NOTHING IS SILENTLY OMITTED. A node or edge field the projection cannot
// produce, and a narrowing that does not apply, are each an error rather than a
// quietly smaller slice — a module that declared a field and received a slice
// without it would compute a wrong answer from a right-looking input, which is
// the failure "bad input always errors" exists to stop.
func (d ContextDeclaration) Validate() error {
	for _, family := range slices.Sorted(maps.Keys(d)) {
		// A RETIRED FAMILY IS REFUSED HERE, WITHOUT THE REGISTRY, and it is the one
		// family name this half can judge. `cloud` and `logs` were supplyable one
		// release ago, so an entry naming one is the entry most likely to exist on
		// an operator's disk right now — and deferring it to the fill path would
		// report a family that WAS valid as one nobody registered, at collect time
		// rather than at the add that wrote it.
		if reason, retired := kgtypes.RetiredGraphTypeReason(family); retired {
			return fmt.Errorf("context declaration: the graph family %q is retired: %s", family, reason)
		}
		if err := d[family].validate(family); err != nil {
			return err
		}
	}
	return nil
}

// ValidateFamilies refuses a declaration naming a family this daemon cannot
// supply, listing the ones it can. supplyable is the fill path's own answer:
// `code` plus every registered graph type.
//
// IT IS A SEPARATE CALL BECAUSE ITS ANSWER IS OPERATOR-TIME, not compile-time. A
// caller that cannot read the registry must not invent an empty supplyable set
// and refuse everything — an unreadable registry is an error at the call site,
// never an empty list, and the fill path is where that distinction is drawn.
func (d ContextDeclaration) ValidateFamilies(supplyable []string) error {
	for _, family := range slices.Sorted(maps.Keys(d)) {
		if slices.Contains(supplyable, family) {
			continue
		}
		return fmt.Errorf(
			"context declaration: no graph family named %q can be supplied; this client supplies %s",
			family, strings.Join(supplyable, ", "))
	}
	return nil
}

// selectsNodes reports whether this declaration admits any node at all: either
// it named types, or it asked for every type.
//
// IT IS THE ONE PREDICATE THE THREE COHERENCE ARMS BELOW ASK, so the selector
// satisfies their reason rather than bypassing it. Each of them refuses a
// declaration asking for node facts with no node to carry them; a family
// draining every type carries nodes, so the fact has somewhere to land.
func (f FamilyDeclaration) selectsNodes() bool {
	return len(f.NodeTypes) > 0 || f.AllNodeTypes
}

// validate checks one family's declared slice.
func (f FamilyDeclaration) validate(family string) error {
	// THE TWO SELECTORS ARE MUTUALLY EXCLUSIVE. A declaration naming types AND
	// asking for every type has said two different things, and either reading
	// silently discards the other — the narrow one drops the types the operator
	// did not name, the wide one drops the narrowing they wrote. Refused by name,
	// at LOAD time, so a hand-written entry is caught by the loader that reads it
	// rather than at the collect that acts on it.
	if f.AllNodeTypes && len(f.NodeTypes) > 0 {
		return fmt.Errorf(
			"context declaration: family %q declares all_node_types AND a node_types list (%s); the two say different "+
				"things — every node of the family, or exactly these types — so declare one of them, not both",
			family, strings.Join(f.NodeTypes, ", "))
	}
	for _, name := range f.NodeFields {
		if !slices.Contains(contextNodeFields, name) {
			return fmt.Errorf(
				"context declaration: family %q declares the node field %q, which this client cannot supply; the node fields it supplies are %s",
				family, name, strings.Join(contextNodeFields, ", "))
		}
	}
	for _, name := range f.EdgeFields {
		if !slices.Contains(contextEdgeFields, name) {
			return fmt.Errorf(
				"context declaration: family %q declares the edge field %q, which this client cannot supply; the edge fields it supplies are %s",
				family, name, strings.Join(contextEdgeFields, ", "))
		}
	}
	// AN INCOHERENT DECLARATION IS REFUSED ON THE SAME RULE AS AN INERT
	// NARROWING. Fields and metadata keys are carried ON nodes, and node_types is
	// what admits a node to the slice at all, so a family naming either with no
	// node types receives no node and the fields it asked for act on nothing.
	//
	// THE EMPTY FORM STAYS LEGAL AND IS NOT THIS CASE: a family declared with
	// nothing at all yields its graph names and no nodes, which is a real
	// declaration — the whole input a membership test over repository names
	// needs. What is refused is asking for node facts with nowhere to put them.
	if !f.selectsNodes() && (len(f.NodeFields) > 0 || len(f.MetadataKeys) > 0) {
		return fmt.Errorf(
			"context declaration: family %q declares node fields or metadata keys but no node_types and no all_node_types, so no node enters the slice to carry them; "+
				"declare node_types or all_node_types, or declare neither and receive the graph names alone",
			family)
	}
	// EDGE FIELDS ARE THE SAME DEFECT ONE LEVEL DOWN, and they are refused on the
	// same rule rather than described by a comment that says they already are.
	//
	// THE EDGE READ IS PIVOTED ON THE PROJECTED NODES' IDS: FetchAllEdges pages
	// over an id set, node_types is what decides which nodes are carried, and the
	// `id` node field is what puts an id on them. A declaration missing either
	// yields a graph with edge_fields declared, zero edges, and NO ERROR — the
	// caller reads "this family has no dependencies" when the truth is "you did
	// not ask in a way that could answer".
	//
	// BOTH HALVES ARE REFUSED, because either alone produces the silent zero: the
	// contextEdgePivotIDs walk skips a node whose ID is empty, so declaring
	// node_types without the id field is as inert as declaring no node_types.
	if len(f.EdgeFields) > 0 {
		if !f.selectsNodes() {
			return fmt.Errorf(
				"context declaration: family %q declares edge_fields but no node_types and no all_node_types. The edge read is "+
					"pivoted on the ids of the nodes that were carried, so with nothing selected nothing is "+
					"carried, no id enters the pivot set, and the family would receive zero edges with no "+
					"error; declare node_types or all_node_types, or drop edge_fields",
				family)
		}
		if !slices.Contains(f.NodeFields, ContextNodeFieldID) {
			return fmt.Errorf(
				"context declaration: family %q declares edge_fields but does not declare the %q node "+
					"field. The edge read is pivoted on the carried nodes' ids, and a node arrives with an "+
					"empty id when the field was not declared, so the family would receive zero edges with "+
					"no error; add %q to node_fields, or drop edge_fields",
				family, ContextNodeFieldID, ContextNodeFieldID)
		}
	}
	// THE ONE FAMILY-SCOPED NARROWING. It is refused where it cannot act rather
	// than accepted and ignored: a declaration that reads as if it narrowed and
	// did not is the same wrong-answer-from-a-right-looking-input failure as an
	// unsupplyable field.
	//
	// EDGE_FIELDS USED TO BE A SECOND ONE, scoped to the cloud family because the
	// cloud subgraph fetch was the only read that returned edges. The fill now
	// reads edges for every family it can read nodes for, through the same
	// graph-type-parameterized edge reader, so scoping the declaration to one
	// family would refuse a correct declaration.
	if family != ContextFamilyCode && len(f.PathBasenames) > 0 {
		return fmt.Errorf(
			"context declaration: family %q declares path_basenames, which narrows file paths and applies to the %q family only",
			family, ContextFamilyCode)
	}
	return nil
}

// CollectContext is the `context` block of the collect input: the foreign-graph
// slice the client filled from the registration's declaration.
//
// IT IS KEYED BY GRAPH-TYPE NAME, the same key the declaration uses, so a module
// reads its slice back out under the name its own entry declared. The key set is
// therefore whatever the operator registered, which is why this is a map and not
// a struct — see the family paragraph at the top of this file.
//
// A FAMILY THAT WAS DECLARED IS ALWAYS PRESENT, even when the daemon holds no
// graph of that type: its value is an empty slice rather than a missing key.
// That is the distinction the whole contract turns on — a declared family with
// nothing in it is an answer the module is entitled to see, while a missing key
// means the entry never asked.
//
// THE SPELLINGS ARE THE CONTRACT'S OWN (id, type, symbol_name, file_path,
// content, metadata, from_id, to_id), so a module author reads one vocabulary on
// the way in and on the way out. graph_name is the exception and it is
// deliberate: it is the per-graph instance key of the read that filled the arm,
// so the block names each slice exactly as the wire that produced it does.
type CollectContext map[string][]GraphContext

// IsEmpty reports whether the block carries no family at all.
func (c CollectContext) IsEmpty() bool { return len(c) == 0 }

// GraphContext is one graph's declared slice.
//
// THE TWO ARRAYS CARRY NO omitempty, unlike every optional field below them, and
// the reason is a name collision worth stating rather than rediscovering. A
// corpus check guards the OUTPUT envelope's three required wire names — nodes,
// edges and walk_complete — against omitempty, because the SDK validates a
// provider's marshaled result against the advertised output schema and a
// dropped required field fails the whole collect on the first empty walk. This
// is an INPUT-side struct and that failure cannot reach it, but the check reads
// wire names and cannot see which side of the wire a struct sits on, so it
// flags this one. Emitting both arrays unconditionally costs a `null` on an
// empty graph, keeps the package clean under that check, and loses nothing: a
// graph's node and edge lists are part of the answer's shape rather than
// optional input the operator supplied.
type GraphContext struct {
	GraphName string        `json:"graph_name"`
	Nodes     []ContextNode `json:"nodes"`
	Edges     []ContextEdge `json:"edges"`
}

// ContextNode is one node projected to the declared field set. Every field is
// omitempty: an undeclared field is ABSENT from the document rather than present
// and empty, which is what makes "exactly the declared slice" observable on the
// wire rather than a promise about the filler.
type ContextNode struct {
	ID         string            `json:"id,omitempty"`
	Type       string            `json:"type,omitempty"`
	SymbolName string            `json:"symbol_name,omitempty"`
	FilePath   string            `json:"file_path,omitempty"`
	Content    string            `json:"content,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ContextEdge is one edge projected to the declared field set.
type ContextEdge struct {
	FromID string `json:"from_id,omitempty"`
	ToID   string `json:"to_id,omitempty"`
}
