// SPDX-License-Identifier: Apache-2.0

package treesitter

// THE DISPOSITION TABLE LIVES IN ITS OWN FILE, apart from the walk that derives
// its subject set. The two grow for different reasons — the walk changes when
// the detector or the trees change, the table grows by one row every time a
// consumer of the Go declaration kinds is added — and keeping them together put
// the pair over this repository's per-file line cap.
// `method_elem` is a NEW declaration kind in the code graph, and every consumer
// keyed on a CLOSED set of Go declaration kinds silently excludes it. A silent
// exclusion is indistinguishable from an oversight, so each such consumer
// carries a stated disposition here.
//
// THE PROPERTY IS "THIS FILE IS GO CODE THAT FILTERS ON A CLOSED SET OF GO
// DECLARATION KINDS". THE SUBJECT SET IS DERIVED FROM THE TREE, never
// hand-listed: the walk in chunker_go_declkind_census_test.go reads THIS
// MODULE's internal tree for non-test .go files naming any of the three Go
// declaration kinds AS A QUOTED GO STRING, and the walk in
// chunker_go_declkind_census_server_test.go reads the server module's. The
// quoting is the detector, and it is what separates a Go string comparison
// from the same token inside a tree-sitter query pattern.
//
// BOTH DETECTOR PROBES WERE RUN AGAINST THE REAL TREE, and this census is the
// worked example of an under-match probe that CONFIRMS a scoping rather than
// breaking it.
//
// OVER-MATCH: the literals are quoted and distinctive; no class of non-consumer
// matches them. The disposition-accuracy check below is the standing guard
// against a row whose file names the new kind only in prose.
//
// UNDER-MATCH: dropping the quotes widens the walk from 20 files to 34, measured
// with `comm -13` between the quoted and unquoted lists. NONE of the 14
// additions is a Go-code kind filter, and every one is ATTRIBUTED rather than
// waved off:
//   - NINE queries_*.go files (csharp, elm, go, java, javascript, kotlin, php,
//     swift, typescript) carry the tokens inside TREE-SITTER QUERY TEXT, where
//     they are that language's own node kinds rather than a Go-vocabulary
//     decision. Covered elsewhere by design: queries_go.go's new arm has its own
//     gate asserting the anchored arm is present and that `method_elem` appears
//     exactly once in the file, and every OTHER queries_*.go is covered by the
//     non-Go invariance test's `no_other_query_names_go_kinds` subtest, which
//     asserts no other query file names method_elem or type_elem and carries
//     queries_go.go as its known-positive control.
//   - FIVE files name the tokens only in DOC COMMENTS, with no filter behind
//     them: ast/result.go, rerank/render.go, chunker_csharp.go (a comment
//     diagramming C#'s attribute shape), chunker_jvm_shared.go (prose describing
//     Java's and Kotlin's `modifiers` shape) and
//     collector/parser/resolve_walk_typed.go. NO GATE COVERS THESE AND NONE IS
//     NEEDED — a comment carries no behavior. Note specifically that
//     `no_other_query_names_go_kinds` does NOT reach chunker_csharp.go or
//     chunker_jvm_shared.go: that subtest scans `queries_*.go` only.
//     resolve_walk_typed.go's comment is the one exception that DOES have a gate,
//     for an unrelated reason — it is the falsified paragraph the CALLS-targeting
//     work rewrites.
//
// EVERY REASON BELOW WAS READ IN CURRENT SOURCE, not inherited from a plan.
type declKindConsumerRow struct {
	// Path is relative to the root the half that declares the row walks:
	// MODULE-relative here (internal/...), REPO-relative in the staging-only
	// server half (cmd/knowledge-server/internal/...). Either way it must match
	// a file that half's walk finds.
	Path        string
	Disposition testCallsDisposition
	// Reason is MANDATORY. A disposition with no reason is indistinguishable
	// from an oversight, which is the precise failure this census exists to
	// catch.
	Reason string
}

// THE TABLE IS SPLIT BY MODULE, and this file carries THIS MODULE's rows with
// MODULE-relative paths — the one spelling correct both here, where the tree is
// cmd/knowledge/internal, and in the published mirror, where the sync script
// copies it to internal/. The server module's single row lives in
// chunker_go_declkind_census_server_test.go, which the sync removes from the
// published tree because the mirror is the client module alone. Both halves run
// in this repository, so the pair covers exactly what the two-tree census did.
var goDeclKindConsumerCensus = []declKindConsumerRow{
	{
		Path:        "internal/collector/treesitter/types.go",
		Disposition: dispositionProducer,
		Reason: "Declares Chunk.ChunkType and documents it as the raw tree-sitter node type. It " +
			"enumerates nothing and filters nothing; a disposition here would be a decision about a doc comment.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_identity.go",
		Disposition: dispositionOptsIn,
		Reason: "declParentName now carries a second Go branch routing method_elem to " +
			"goInterfaceParentName, so a spec takes its interface as ParentName by the same rule a " +
			"method takes its receiver. classLikeByLang is deliberately untouched: its Go row is " +
			"empty, and admitting a kind there would change parent resolution for every Go declaration.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_go.go",
		Disposition: dispositionOptsIn,
		Reason: "Declares goInterfaceParentName, the method_elem ascent, beside extractGoReceiver. " +
			"classifyTestKindGo in the same file routes any non-function_declaration in a _test.go " +
			"file to TestKindHelper, so a spec in a test file classifies as helper — the same " +
			"treatment a method_declaration already gets there, and intended rather than incidental.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_test_kind.go",
		Disposition: dispositionExcluded,
		Reason: "Holds the testKindClassifier SIGNATURE and the per-language registry; the kinds appear " +
			"only in its doc comment describing the chunkType argument. The Go classification decision " +
			"itself lives in chunker_go.go and is censused there.",
	},
	{
		Path:        "internal/collector/treesitter/chunker.go",
		Disposition: dispositionOptsIn,
		Reason: "emitDeclarationChunk sets Context.Signature for Go on function_declaration, " +
			"method_declaration AND method_elem, so a spec renders the same human-readable signature " +
			"every sibling declaration kind renders. extractGoSignature needs no method_elem case: a " +
			"spec has no `body` field and the no-body branch returns the whole node, which is exactly " +
			"a spec's signature text. The COMPOSED signature is a separate carrier — TypeFacts.Sig, " +
			"which is what the satisfaction derivation reads — and is censused on " +
			"chunker_go_typefacts.go.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_kind_symbols.go",
		Disposition: dispositionOptsIn,
		Reason: "The Go kind-class TABLE: it maps node-kind spellings to class codes so a hot-path arm " +
			"classifies by SYMBOL ID rather than by a cgo call returning a fresh string. The " +
			"conversion this row previously named as a follow-up has been done, under the allocation " +
			"measurement that justifies it: method_elem, interface_type and type_parameter_list are " +
			"here now, alongside the signature-composition vocabulary chunker_go_sig.go names. " +
			"type_spec is the ONE kind that follow-up predicted and this table still does NOT carry, " +
			"and the omission is deliberate: goSoleTypeSpec — the only reader of that spelling — is " +
			"reached by extractGoEmbeds and extractGoInterfaceEmbeds as well, so it runs on the " +
			"un-armed benchmark leg too. Converting it would cut real allocations while LOWERING the " +
			"denominator of the arm's ratio budget. The fence is arm-exclusivity, and it is stated in " +
			"full on the const block in the file itself.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_go_typefacts.go",
		Disposition: dispositionOptsIn,
		Reason: "goTypeFacts carries a method_elem arm setting Sig and nothing else: a spec declares " +
			"no locals and no fields, and its results are half of its IDENTITY rather than a call's " +
			"value. The type_declaration arm also records IsInterface, read from the type_spec's " +
			"`type` field and declining a grouped declaration outright.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_emit.go",
		Disposition: dispositionExcluded,
		Reason: "Only the EMBEDS arm is kind-gated (chunkType == \"type_declaration\"), and it stays " +
			"that way because a METHOD SPEC has no embedded elements of its own — an interface's " +
			"embeds belong to the interface declaration. Every other arm in the file is kind-agnostic " +
			"and already serves a spec chunk: CONTAINS resolves POSITIONALLY, and for a spec it " +
			"resolves BETTER than for a Go method, because the enclosing type_declaration is itself a " +
			"chunked declaration so containerSlot finds its slot and FromChunk is non-zero. CALLS and " +
			"USES_TYPE emit from the spec's own FromID; those are DIFFERENT edges from the enclosing " +
			"type's, not duplicates, so a higher USES_TYPE count is the intended finer grain.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_java_kinds.go",
		Disposition: dispositionExcluded,
		Reason: "A per-language SYMBOL-CLASS TABLE for the JAVA grammar: it maps java node-kind " +
			"spellings onto class codes so the java arms classify by symbol id rather than by a cgo " +
			"call returning a fresh string. \"method_declaration\" here is JAVA's own kind, and " +
			"method_elem is a Go kind that cannot appear in a java parse tree. The census detector " +
			"matched this file because the quoted token is SHAPED like a Go kind name, not because " +
			"the file filters Go declaration kinds.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_kotlin_kinds.go",
		Disposition: dispositionExcluded,
		Reason: "A per-language SYMBOL-CLASS TABLE for the KOTLIN grammar. \"function_declaration\" here is " +
			"KOTLIN's own kind — it covers both a concrete function and an interface member — and " +
			"method_elem is a Go kind that cannot appear in a kotlin parse tree. The census detector " +
			"matched this file because the quoted token is SHAPED like a Go kind name.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_scala_kinds.go",
		Disposition: dispositionExcluded,
		Reason: "A per-language SYMBOL-CLASS TABLE for the SCALA grammar. \"function_declaration\" here " +
			"is SCALA's own kind for a trait's ABSTRACT member, distinct from function_definition, " +
			"and method_elem is a Go kind that cannot appear in a scala parse tree. The census " +
			"detector matched this file because the quoted token is SHAPED like a Go kind name.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_csharp_kinds.go",
		Disposition: dispositionExcluded,
		Reason: "A per-language SYMBOL-CLASS TABLE for the C# grammar. \"method_declaration\" here is " +
			"C#'s own kind, and method_elem is a Go kind that cannot appear in a csharp parse tree. " +
			"The census detector matched this file because the quoted token is SHAPED like a Go kind " +
			"name.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_php_kinds.go",
		Disposition: dispositionExcluded,
		Reason: "A per-language SYMBOL-CLASS TABLE for the PHP grammar. \"method_declaration\" here is " +
			"PHP's own kind, and method_elem is a Go kind that cannot appear in a php parse tree. " +
			"The census detector matched this file because the quoted token is SHAPED like a Go kind " +
			"name.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_groovy.go",
		Disposition: dispositionExcluded,
		Reason: "resolveDeclNameGroovy switches on GROOVY chunk kinds — class_definition, " +
			"function_definition and function_declaration, the last being an interface's abstract " +
			"member — to recover a name the query set binds no capture for. The kinds are that " +
			"grammar's own vocabulary; a Go method spec cannot appear in a groovy parse tree.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_groovy_kinds.go",
		Disposition: dispositionExcluded,
		Reason: "A per-language SYMBOL-CLASS TABLE for the GROOVY grammar. \"function_declaration\" " +
			"here is GROOVY's own kind for an interface's ABSTRACT member, distinct from " +
			"function_definition, and method_elem is a Go kind that cannot appear in a groovy parse " +
			"tree. The census detector matched this file because the quoted token is SHAPED like a " +
			"Go kind name.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_elm.go",
		Disposition: dispositionExcluded,
		Reason: "\"type_declaration\" here is ELM's own grammar kind in the Elm declaration-name " +
			"resolver, beside type_alias_declaration. It is not Go's vocabulary and shares only the spelling.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_javascript_imports.go",
		Disposition: dispositionExcluded,
		Reason: "Filters JavaScript's function_declaration / class_declaration / " +
			"generator_function_declaration while resolving re-export targets. A Go interface's " +
			"method spec cannot appear in a JavaScript parse tree.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_ecma_kinds.go",
		Disposition: dispositionExcluded,
		Reason: "The typescript / tsx / javascript kind-class TABLES. \"function_declaration\" here is " +
			"the ECMAScript grammars' own kind, mapped to an ECMAScript class code and read only by " +
			"the ECMAScript arms. A Go interface's method spec cannot appear in any of those three " +
			"parse trees, and naming method_elem here would panic at first use: newSymbolClasses " +
			"requires every mapped name to be a REGULAR symbol of the grammar the table is built for.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_ecma_typefacts.go",
		Disposition: dispositionExcluded,
		Reason: "ecmaTypeFacts switches on the ECMAScript chunkType vocabulary — its " +
			"\"function_declaration\" is the ECMAScript kind, alongside method_definition and " +
			"method_signature. Its interface arm is TypeScript's interface_declaration, whose members " +
			"are method_signature and property_signature; method_elem is Go's spelling for the same " +
			"idea and never reaches this arm, because the type-facts registry dispatches by language " +
			"before any kind is read.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_php.go",
		Disposition: dispositionExcluded,
		Reason: "Gates on PHP's method_declaration / function_definition for PHP signature extraction. " +
			"Different grammar; Go kinds never reach it.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_swift.go",
		Disposition: dispositionExcluded,
		Reason: "Gates on Swift's function_declaration for Swift signature extraction. Different " +
			"grammar; Go kinds never reach it.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_swift_qualtypes.go",
		Disposition: dispositionExcluded,
		Reason: "swiftKindNames maps SWIFT node-kind spellings to swift class codes, and swift spells " +
			"two of its kinds the same way Go spells its own — function_declaration and " +
			"property_declaration. The table is built against the swift grammar, so a Go node can " +
			"never be classified through it and method_elem, a Go kind, can never reach it. This row " +
			"exists because the walk's detector is the QUOTED spelling, which cannot tell one " +
			"grammar's vocabulary from another's once per-language kind tables exist.",
	},
	{
		Path:        "internal/tools/ast_hydrator.go",
		Disposition: dispositionExcluded,
		Reason: "functionishTypeSet enumerates CALLABLE declarations with bodies, so an ast match " +
			"inside an interface body still reports no enclosing functionish declaration — unchanged " +
			"by this work. Adding method_elem would name a spec as the enclosing declaration of a " +
			"match, and a spec encloses no statements: what a reader wants at that position is the " +
			"interface declaration, which is not functionish either. The set stays as it is.",
	},
	{
		Path:        "internal/tools/ast_handlers.go",
		Disposition: dispositionExcluded,
		Reason: "The kind appears only in a comment about bare-kind where-leaves. The ast where-tree's " +
			"`kind` leaf is validated against the LANGUAGE'S OWN node-kind vocabulary from tree-sitter, " +
			"not against any list in this file, so `method_elem` is already expressible with no change.",
	},
	{
		Path:        "internal/tools/ast_schema.go",
		Disposition: dispositionExcluded,
		Reason: "The kinds appear only as EXAMPLES in the tool's JSON-schema prose. Examples teach the " +
			"shape of a kind leaf; they do not constrain which kinds are accepted.",
	},
	{
		Path:        "internal/tools/help_content_ast.go",
		Disposition: dispositionExcluded,
		Reason:      "Same as the schema: the kinds appear only as examples in help text, with no filter behind them.",
	},
	{
		Path:        "internal/ast/where_json.go",
		Disposition: dispositionExcluded,
		Reason: "The kind appears once inside a doc comment illustrating a malformed where-leaf. The " +
			"parser accepts any kind string and defers validation to the language's node-kind vocabulary.",
	},
	{
		Path:        "internal/topology/dead_code_review.go",
		Disposition: dispositionExcluded,
		Reason: "functionishTypes drives the dead-code join, and a method spec is deliberately absent. " +
			"WHAT A USER SEES: interface method specs never appear in the dead-code review. That is " +
			"correct — an uncalled spec is not dead the way an uncalled function is, since the whole " +
			"point of a contract declaration is that its callers reach it through the interface while " +
			"its bodies live on the implementers. Listing specs would report every single-implementer " +
			"contract as dead code.",
	},
	{
		Path:        "internal/topology/graph/god_object.go",
		Disposition: dispositionExcluded,
		Reason: "typeishNodeTypes enumerates CLASS-LIKE declarations; method_elem is a MEMBER, and " +
			"adding it would make every interface method its own god-object candidate. Go's " +
			"type_declaration, struct_type and interface_type are already listed, so an interface " +
			"remains a candidate as before. The one real consequence is on the members side and it is " +
			"wanted: a Go interface's CONTAINS set now includes its specs, so its member count reflects " +
			"the true size of the method set instead of zero.",
	},
	{
		Path:        "internal/topology/corpusscan/assertion.go",
		Disposition: dispositionExcluded,
		Reason: "It filters on NO closed set of declaration kinds at all: a graph-shaped corpus check " +
			"names its node_type as free text in the check body, and the parser validates only that the " +
			"value is non-empty, precisely because tree-sitter kinds are an open per-grammar vocabulary. " +
			"So method_elem is already expressible by a corpus author with no code change here, and a " +
			"closed list would be the thing that broke it. The quoted kind that puts this file in the " +
			"subject set is the example in node_type's doc comment. The absence of a candidate node of " +
			"the requested kind is caught at runtime by the non-empty-candidate-set control in " +
			"exec_graph.go rather than by a compile-time enumeration.",
	},
}
