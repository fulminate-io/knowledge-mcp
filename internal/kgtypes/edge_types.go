// SPDX-License-Identifier: Apache-2.0

package kgtypes

// Code edge types (uppercase, from static analysis).
const (
	EdgeCalls    EdgeType = "CALLS"
	EdgeImports  EdgeType = "IMPORTS"
	EdgeContains EdgeType = "CONTAINS"
	EdgeUsesType EdgeType = "USES_TYPE"

	// EdgeImplements records that a concrete type satisfies an interface,
	// derived syntactically from the declaration index rather than from a type
	// checker. It is emitted at TWO LEVELS: interface type declaration → concrete
	// type declaration, and interface method spec → the method satisfying it.
	//
	// DIRECTION IS FROM THE INTERFACE OUTWARD, which is what makes the two-hop
	// model work: a caller standing on a call's target — the interface method —
	// reaches the implementers with one outbound traversal, instead of the call
	// itself fanning out across every type declaring a method of that name.
	//
	// IT HAS TWO DERIVATIONS, and Edge.Method is what tells them apart. Go
	// METHOD-SET MATCHING infers satisfaction the language leaves implicit, by
	// comparing resolved signatures, and stamps EdgeMethodMethodSet. DECLARED
	// CONFORMANCE reads a supertype clause the source WROTE — an implements, an
	// extends, a mixin, a behaviour, a trait — and stamps
	// EdgeMethodDeclaredConformance followed by that clause's kind. Both levels
	// and the direction above are identical for either derivation; only the
	// question each answers differs, so a consumer that cares reads the prefix
	// rather than the edge type.
	//
	// EdgeType is a defined string type and the vocabulary is open, so this
	// constant needs no proto change; TEST_CALLS is the in-tree precedent. The
	// producer mirrors it as treesitter.EdgeImplements, and the two are pinned in
	// lockstep by TestImplementsVocabularyLockstep.
	EdgeImplements EdgeType = "IMPLEMENTS"

	// EdgeTestCalls is a CALLS edge whose SOURCE is test code: the body of a
	// test_block chunk, or a declaration lexically inside one. It is a distinct
	// type rather than a flag on EdgeCalls so that every existing CALLS
	// consumer — centrality, blast radius, the god-object metrics — keeps
	// seeing production call structure only, and so a consumer that WANTS test
	// traffic opts into it explicitly. EdgeType is a defined string type and
	// the vocabulary is open, so this constant needs no proto change.
	//
	// IT MEANS "test-origin AND identifiable as such", NOT "all test-origin".
	// Identifiability is range containment inside a test_block chunk, so the 18
	// languages with no TestBlocks query have no test_block range for their
	// test declarations to sit inside and their edges stay EdgeCalls. A
	// consumer that opts out of TEST_CALLS still sees that residue as CALLS.
	//
	// The producer mirrors this constant as treesitter.EdgeTestCalls (the
	// chunker carries its own EdgeType vocabulary); the two are pinned in
	// lockstep by TestTestCallsConsumerCensus.
	EdgeTestCalls EdgeType = "TEST_CALLS"

	// EdgeLanguage links a code symbol to its per-language hub node
	// (NodeLanguage with deterministic ID lang:<repo>:<lang>). Emitted
	// once per non-comment chunk during indexing so topology analyzers
	// can roll up PageRank / fan-in / etc. per language via a single
	// outbound traversal from each symbol.
	//
	// File → symbol membership is NOT duplicated as a reverse edge:
	// callers reverse-walk the existing EdgeContains (file → symbol)
	// with direction="in" instead of materializing a separate
	// "defined-in" edge type.
	EdgeLanguage EdgeType = "LANGUAGE"
)

// Knowledge edge types (lowercase, for knowledge graph relationships).
const (
	EdgeKGContains   EdgeType = "contains"     // parent → child (plan → phase, phase → step)
	EdgeDependsOn    EdgeType = "depends-on"   // must complete before
	EdgeVerifies     EdgeType = "verifies"     // criterion → step
	EdgeInformedBy   EdgeType = "informed-by"  // decision ← finding/research
	EdgeSupports     EdgeType = "supports"     // evidence → decision
	EdgeAnswers      EdgeType = "answers"      // finding → research question
	EdgeRelatesTo    EdgeType = "relates-to"   // general association
	EdgeKGImplements EdgeType = "implements"   // step → code resource
	EdgeReferences   EdgeType = "references"   // finding → reference (paper/URL)
	EdgeUses         EdgeType = "uses"         // agent/skill → tool_guide it relies on
	EdgeAudits       EdgeType = "audits"       // ticket/plan → language_pattern (defensive — "audit the implementation against this anti-pattern")
	EdgeConstrains   EdgeType = "constrains"   // rule → agent/skill it governs
	EdgeInstantiates EdgeType = "instantiates" // project pattern → library pattern (concrete instantiation of a canonical pattern)
	EdgeAppliesWhen  EdgeType = "applies-when" // pattern → use_case condition under which the pattern should be applied
	EdgeAvoidWhen    EdgeType = "avoid-when"   // pattern → use_case condition under which the pattern should be avoided
	// EdgeTranslatedFrom records provenance from a node in a target domain graph
	// (e.g. practice/design-patterns) back to the source node it was synthesized
	// from (in any source graph type — web, code, cloud, knowledge, …) during a
	// transformer run. Evidence carries the source slug.
	EdgeTranslatedFrom EdgeType = "translated-from" // target-domain node → source node (transformer provenance)

	// EdgeMetaValue links a node to a shared value-node that holds the
	// actual content for one of its metadata keys. Emitted by the
	// self-tuning metadata storage layer (plan T1) when the per-graph
	// promotion registry decides a key should be stored as edges instead
	// of inline scalars in Node.Metadata. The metadata key itself is
	// stored on Edge.Method so a single edge type can carry every
	// promoted key without polluting the EdgeType enum.
	//
	// Reads via Node.Value(key) traverse outgoing EdgeMetaValue edges
	// whose Method matches key; writes via Node.SetValue dedupe the
	// value-node by deterministic ID (see valueNodeID). Distinct from
	// EdgeReferences (generic node references) so traverse filters can
	// isolate metadata edges cleanly.
	EdgeMetaValue EdgeType = "meta_value" // node → value-node (metadata key on Edge.Method)

	// EdgeMethodAmbiguousName and EdgeMethodDynamic are Edge.Method VALUES, not
	// edge types. Every member of one group also shares a key in Edge.Evidence.
	//
	// Edge.Method POPULATIONS ARE KEYED BY EDGE TYPE.
	//
	// The field is not one vocabulary with a fixed number of meanings; it is a
	// per-edge-type slot, and which population a value belongs to is decided by
	// the edge that carries it. THE RULE IS SCOPED TO THE CODE COLLECTOR'S OWN
	// EDGES — Edge.Method carries unrelated values elsewhere in this same file,
	// where EdgeMetaValue puts a promoted metadata KEY on it — so nothing below
	// is a claim about every edge in every graph.
	//
	// The populations the code collector emits today:
	//
	//  1. GROUP KIND — one of the two constants declared here, on every member
	//     of a multi-candidate group. Emitted for reference edges (CALLS,
	//     TEST_CALLS, USES_TYPE, EMBEDS) AND for the ambiguous Go-receiver
	//     containment case, so it is NOT exclusive to reference edges: a CONTAINS
	//     edge whose receiver type resolved to several candidates carries a group
	//     kind too.
	//  2. RESOLVING RUNG — the name of the resolution rule that bound the
	//     reference, on a BOUND reference edge, so a surprising edge is
	//     attributable at read time. The vocabulary is the collector's own RefRule
	//     constant set in cmd/knowledge/internal/collector/parser/resolve_walk.go,
	//     which is the single authority for it; the values are deliberately NOT
	//     restated here, because a copy of a vocabulary is a copy that goes stale.
	//     Bound edges only: a single-candidate containment arm emits no Method.
	//  3. METHOD-SET CARDINALITY — on an IMPLEMENTS edge, the EdgeMethodMethodSet
	//     prefix followed by the interface's expanded method-set size. A DERIVED
	//     edge rather than a resolved reference: it never enters the resolution
	//     walk, so it carries neither a group kind nor a rung.
	//  4. DECLARED CLAUSE KIND — on an IMPLEMENTS edge derived from a supertype
	//     clause the source WROTE, the EdgeMethodDeclaredConformance prefix
	//     followed by that clause's kind. Also a DERIVED edge, and never a
	//     cardinality: the clause states the relationship outright, so no method
	//     set was measured and publishing a number would be a false statement in
	//     the one field consumers weight on.
	//
	// THE SET IS OPEN, AND ADDING TO IT NEEDS NO EDIT HERE beyond a new member.
	// Do not restate a total: a count of populations is exactly the sentence a new
	// one falsifies, in every file that repeats it.
	//
	// THE TWO GROUP KINDS ARE NOT INTERCHANGEABLE. An EdgeMethodAmbiguousName
	// group is CLOSED — the reference means exactly one of these candidates, and
	// a consumer that later learns which may collapse the group to it. An
	// EdgeMethodDynamic group is OPEN — the reference dispatches to one of these
	// candidates OR to something no static enumeration can reach, so a consumer
	// must never read it as closed and must never collapse it.
	//
	// EMPTY METHOD ON A BOUND EDGE.
	//
	// On a graph collected since bound-edge attribution landed, EVERY bound edge
	// carries its rung: the server's edge-meta comparison is Method-aware, so a
	// resident edge whose incoming twin differs only in Method is rewritten
	// rather than skipped. An empty Method on a bound edge there is a REAL
	// SIGNAL — that edge is unattributed — and not an artifact of when it was
	// first written. A graph NOT collected since is the one exception: it still
	// holds pre-stamp bound edges, and on that graph alone an empty Method is
	// ambiguous. One collect ends that state; it is transitional and never a
	// property of the field.
	//
	// They live here rather than in the collector because Edge.Method is a
	// persisted wire field: a reader deciding whether a group may be collapsed
	// needs the vocabulary without importing the producer.
	EdgeMethodAmbiguousName = "ambiguous-name" // CLOSED group: exactly one of the members is the referent
	EdgeMethodDynamic       = "dynamic"        // OPEN group: one of the members, or something beyond static reach

	// EdgeMethodMethodSet is the PREFIX of the Edge.Method value an IMPLEMENTS
	// edge carries: the prefix followed by the decimal cardinality of the
	// interface's expanded method set, e.g. "method-set:3".
	//
	// IT IS THE SURFACE A CONSUMER READS TO WEIGHT A ONE-METHOD EDGE AS
	// LOW-INFORMATION. A single-method interface is legitimately satisfied by a
	// great many types — that is correct Go, not a defect to suppress — so the
	// cardinality is published rather than used to filter.
	//
	// THE CARDINALITY IS NOT CARRIED ON Weight, DELIBERATELY. The weighted
	// topology analyzers normalize a zero weight to the 1.0 baseline, so putting
	// the size on Weight would INVERT the intent: the low-information
	// single-method edges would enter weighted centrality at exactly an ordinary
	// edge's strength, while a large interface's edge took many times an ordinary
	// edge's random-walker mass. No weighted analyzer reads Method, which is why
	// it is the right home — the same reason the two group-kind values above live
	// here rather than in the collector.
	EdgeMethodMethodSet = "method-set:" // IMPLEMENTS: prefix + expanded method-set cardinality

	// EdgeMethodDeclaredConformance is the PREFIX of the Edge.Method value an
	// IMPLEMENTS edge derived from a DECLARED supertype clause carries: the
	// prefix followed by the kind of clause the source wrote, e.g.
	// "declared-conformance:mixin".
	//
	// IT CARRIES A CLAUSE KIND RATHER THAN A CARDINALITY, and the distinction is
	// the point. The method-set derivation INFERS satisfaction the language
	// leaves implicit, so the size of the set it matched is the honest measure
	// of how much the edge says. A declared clause states the relationship
	// outright, so there is no measured set — the informative fact is WHICH
	// clause was written, which is what lets a consumer tell a module include
	// from an implements clause without knowing the producing language.
	//
	// THE MEMBER-LEVEL EDGE CARRIES THE SAME VALUE AS ITS TYPE-LEVEL PARENT,
	// byte-for-byte, mirroring the method-set derivation's own contract: one
	// value is computed per pair and stamped on the type-level edge and on every
	// member edge under it.
	EdgeMethodDeclaredConformance = "declared-conformance:" // IMPLEMENTS: prefix + the declared clause kind

	// EdgeMethodSlotBind is the PREFIX of the Edge.Method value an IMPLEMENTS
	// edge derived from a C COMPOSITE-LITERAL SLOT carries: the prefix followed
	// by the capture shape that produced it, either "slot-bind:designated" or
	// "slot-bind:positional".
	//
	// IT IS C'S CONFORMANCE, WRITTEN THE ONLY WAY C CAN WRITE ONE. The language
	// declares no supertype and has no clause to read; what it has is a struct
	// of function pointers filled by a literal, and that field-to-function pair
	// states the same relationship a declared conformance states outright.
	//
	// THE SUFFIX IS THE SHAPE RATHER THAN THE SLOT NAME. A reader judging how
	// much the edge says needs to know whether the source named the field
	// outright or whether the field was derived from the declaration's field
	// ORDER — the second is exact but rests on one more inference — and the
	// slot name is already recoverable from the edge's own endpoint.
	EdgeMethodSlotBind = "slot-bind:" // IMPLEMENTS: prefix + the capture shape

	// Hive work-queue edge types (cloud-only feature). NEW edges — NOT reuse of
	// EdgeKGContains: EdgeKGContains="contains" is parent→child (plan→phase),
	// the OPPOSITE direction, so reusing it would invert the semantics. The wire
	// literals mirror the server store vocabulary
	// (cmd/knowledge-server/internal/store/edge_types_vocab.go) verbatim — a
	// deliberate per-module duplicate (no shared package); per-module
	// drift-guard tests pin the two copies in lockstep.
	EdgeContainedBy EdgeType = "contained-by" // child → parent (message → hive, hive_member → hive)
	EdgeRespondsTo  EdgeType = "responds-to"  // result message → the original message it answers (ack reply)

	// Thought graph edge types.
	EdgeNext            EdgeType = "next"             // sequential thought chain
	EdgeBranchesFrom    EdgeType = "branches-from"    // new direction after invalidation
	EdgeChargedBy       EdgeType = "charged-by"       // thought → charge
	EdgeEvidencedBy     EdgeType = "evidenced-by"     // charge → evidence artifact
	EdgeProduced        EdgeType = "produced"         // thought → artifact it created
	EdgeSynthesizedFrom EdgeType = "synthesized-from" // finding → original thoughts it was synthesized from
	// EdgeBecause: from=consequence, to=cause. A —because→ B reads
	// "A is true/happens because B is true/happens." Distinct from
	// EdgeRelatesTo (general association) and EdgeInformedBy (evidential support).
	EdgeBecause EdgeType = "because" // consequence → cause (causal/explanatory link)

	// Cloud infrastructure edge types (uppercase, from resource JSON analysis).
	EdgeMountsSecret    EdgeType = "MOUNTS_SECRET"    // workload → secret (volume or env ref)
	EdgeMountsConfigMap EdgeType = "MOUNTS_CONFIGMAP" // workload → configmap (volume or env ref)
	EdgeUsesSA          EdgeType = "USES_SA"          // workload → serviceaccount
	EdgeUsesPVC         EdgeType = "USES_PVC"         // workload → persistentvolumeclaim
	EdgeSelects         EdgeType = "SELECTS"          // service → workload (label selector match)
	EdgeRoutesTo        EdgeType = "ROUTES_TO"        // ingress → service (backend ref)
	EdgeInNamespace     EdgeType = "IN_NAMESPACE"     // namespaced resource → Namespace node (structural membership)
	EdgeRunsInCluster   EdgeType = "RUNS_IN_CLUSTER"  // k8s resource → Cluster proxy (GKE/EKS/AKS, cross-graph linkage via postpopulate_cluster.go and cmd/knowledge/internal/collector/cloud/k8s/cluster_proxy_emit.go)
	EdgeRunsOn          EdgeType = "RUNS_ON"          // pod → node (scheduled pod runs on k8s Node; pod.Spec.NodeName emitted by sub_pods.go)
	EdgeBackedByVM      EdgeType = "BACKED_BY_VM"     // k8s Node → VM proxy (cross-graph linkage via postpopulate_nodes.go)

	// NetworkPolicy / firewall reachability edges (post-resolution).
	//
	// These edges are NOT raw NetworkPolicy metadata — they are emitted by
	// reachability analyzers after resolving label selectors, namespace
	// selectors, ipBlocks, and default-deny semantics into concrete pod→pod
	// relationships. The AWS Security Group Reachability analyzer reuses the
	// Ingress/Egress variants for SG-to-resource reachability edges per the
	// cross-plan alignment decision.
	EdgeRestrictsIngress  EdgeType = "RESTRICTS_INGRESS"   // networkpolicy → pod (policy selects pod as ingress target)
	EdgeRestrictsEgress   EdgeType = "RESTRICTS_EGRESS"    // networkpolicy → pod (policy selects pod as egress source)
	EdgeAllowsIngressFrom EdgeType = "ALLOWS_INGRESS_FROM" // dst_pod → src_pod; dst allows ingress from src (also used by AWS SG reachability)
	EdgeAllowsEgressTo    EdgeType = "ALLOWS_EGRESS_TO"    // src_pod → dst_pod; src allows egress to dst (also used by AWS SG reachability)

	// AdminNetworkPolicy variants. Distinct edge types so multiple ANP rules
	// can co-exist on the same (src,dst) pair as a regular NetworkPolicy edge
	// without overwriting its metadata in the (FromID,Type,ToID)-keyed
	// edgeMeta map. The K8s reachability analyzer evaluates these edges
	// FIRST and applies priority dispatch (Allow / Deny / Pass) before
	// falling through to ALLOWS_INGRESS_FROM / ALLOWS_EGRESS_TO.
	EdgeANPIngressFrom EdgeType = "ANP_INGRESS_FROM" // dst_pod → src_pod; AdminNetworkPolicy ingress entry
	EdgeANPEgressTo    EdgeType = "ANP_EGRESS_TO"    // src_pod → dst_pod; AdminNetworkPolicy egress entry

	EdgeScales            EdgeType = "SCALES"              // hpa → workload (scaleTargetRef)
	EdgeBindsRole         EdgeType = "BINDS_ROLE"          // rolebinding → role/clusterrole
	EdgeBindsSubject      EdgeType = "BINDS_SUBJECT"       // rolebinding → serviceaccount
	EdgeMemberOf          EdgeType = "MEMBER_OF"           // iam-user → iam-group membership
	EdgeHasMember         EdgeType = "HAS_MEMBER"          // iam-group → iam-user; forward direction of EdgeMemberOf for efficient group membership traversal.
	EdgeUsesStorageClass  EdgeType = "USES_STORAGE_CLASS"  // pvc → storageclass
	EdgeGrants            EdgeType = "GRANTS"              // iam binding → service account
	EdgeUsesNetwork       EdgeType = "USES_NETWORK"        // subnet → network/VPC
	EdgeUsesSubnet        EdgeType = "USES_SUBNET"         // instance/nic → subnet
	EdgeUsesSecurityGroup EdgeType = "USES_SECURITY_GROUP" // instance/nic → security group/NSG
	EdgeTargets           EdgeType = "TARGETS"             // load balancer → target group/backend
	EdgeAssumesRole       EdgeType = "ASSUMES_ROLE"        // instance/function → IAM role
	EdgeWorkloadIdentity  EdgeType = "WORKLOAD_IDENTITY"   // k8s SA → GCP SA (GKE workload identity)
	EdgeAssumesIdentity   EdgeType = "ASSUMES_IDENTITY"    // k8s ServiceAccount → IAM identity (cross-graph proxy; GCP SA, IRSA role, Azure managed identity)
	EdgeConnectsTo        EdgeType = "CONNECTS_TO"         // workload → external cloud service endpoint (scanned from env var URIs)
	EdgeUsesDisk          EdgeType = "USES_DISK"           // k8s PersistentVolume → underlying cloud disk (EBS volume, GCE PD, Azure Disk)
	EdgeIssuedBy          EdgeType = "ISSUED_BY"           // certificate → issuer/clusterissuer
	EdgeUsesMiddleware    EdgeType = "USES_MIDDLEWARE"     // ingressroute → middleware (Traefik)
	EdgeSinksTo           EdgeType = "SINKS_TO"            // logging sink → destination bucket/topic
	EdgeSubscribesTo      EdgeType = "SUBSCRIBES_TO"       // subscription → topic (Pub/Sub)
	EdgeOwnedBy           EdgeType = "OWNED_BY"            // child → parent via k8s ownerReference
	EdgeHasEndpointSlice  EdgeType = "HAS_ENDPOINT_SLICE"  // Service → EndpointSlice via kubernetes.io/service-name label
	EdgeBacks             EdgeType = "BACKS"               // EndpointSlice → Pod via endpoint.targetRef (ready/serving/terminating on edge metadata)
	EdgeBoundTo           EdgeType = "BOUND_TO"            // PVC → PV, EBS volume → EC2 instance
	EdgeEncryptsWith      EdgeType = "ENCRYPTS_WITH"       // resource → KMS key (server-side encryption)
	EdgeTriggers          EdgeType = "TRIGGERS"            // event source → function (SQS→Lambda, DynamoDB→Lambda)
	EdgeDeadLettersTo     EdgeType = "DEAD_LETTERS_TO"     // queue → dead-letter queue (SQS RedrivePolicy)
	EdgeReferencesStore   EdgeType = "REFERENCES_STORE"    // external secret → secret store (external-secrets CRD)
	EdgeProtects          EdgeType = "PROTECTS"            // security policy → backend service (Cloud Armor, WAF)
	EdgeUsesCert          EdgeType = "USES_CERT"           // resource → ACM/TLS certificate (ALB, CloudFront, API Gateway)
	EdgeMonitors          EdgeType = "MONITORS"            // alarm → resource being monitored (CloudWatch alarm dimensions)
	EdgeUsesImage         EdgeType = "USES_IMAGE"          // workload → container registry repo (K8s Deployment→ECR, ECS→ECR)
	EdgeTrusts            EdgeType = "TRUSTS"              // trusted principal → IAM role (cross-account AssumeRole trust)
	EdgeSharedWith        EdgeType = "SHARED_WITH"         // host VPC resource → service project (GCP Shared VPC)

	// AWS network-layer edge types for Security Group reachability v2.
	//
	// EdgeAssociatedWithSubnet links a subnet to its controlling Network ACL
	// (1:1 — every subnet belongs to exactly one NACL). The AWS SG reachability
	// analyzer evaluates NACL rules at the subnet layer BEFORE checking SG
	// rules so a packet is allowed iff (SG layer allows) AND (NACL layer allows).
	//
	// Cross-VPC reachability edges:
	//  - EdgePeeredWith   : VPC ↔ VPC via an active peering connection
	//  - EdgeRoutesVia    : subnet/VPC → Transit Gateway attachment
	//  - EdgeExposedVia   : service → VPC endpoint (PrivateLink)
	//  - EdgeRoutesToPeer : subnet → peer VPC CIDR range via a route table entry
	EdgeAssociatedWithSubnet EdgeType = "ASSOCIATED_WITH_SUBNET" // subnet → NACL (reverse of NACL → subnet)
	EdgePeeredWith           EdgeType = "PEERED_WITH"            // VPC → peer VPC via peering connection
	EdgeRoutesVia            EdgeType = "ROUTES_VIA"             // subnet/VPC → transit gateway attachment
	EdgeExposedVia           EdgeType = "EXPOSED_VIA"            // service → VPC endpoint (PrivateLink)
	EdgeRoutesToPeer         EdgeType = "ROUTES_TO_PEER"         // subnet → peer VPC CIDR (route table entry)

	// EdgeExposedBy links a K8s Service / Ingress / Gateway to the cross-graph
	// proxy of the cloud load balancer (GCP forwardingRule, AWS ELB) that
	// realizes its public address. Emitted by the per-kind resolvers in
	// cloud/k8s/postpopulate_services.go, postpopulate_ingress.go, and
	// postpopulate_gateway.go via the shared index helpers in
	// postpopulate_cloud_lb_index.go.
	//
	// Distinct from EdgeExposedVia (service → PrivateLink VPC endpoint) — that
	// edge models AWS PrivateLink consumer-side exposure and is unrelated.
	EdgeExposedBy EdgeType = "EXPOSED_BY" // K8s Service/Ingress/Gateway → cloud LB resource (cross-graph proxy)

	// Singleton-subcollector edges.
	//
	// These constants land unused in this phase — downstream phases (AWS SES /
	// CloudWatch / ACM / DynamoDB, GCP disk / artifact registry / firestore,
	// Azure Key Vault / App Service) wire real emitters that reference them.
	EdgeNotifiesVia  EdgeType = "NOTIFIES_VIA"  // alarm/metric → notification target (CloudWatch→SNS/Lambda/SQS/ASG; GCP alert policy→channel; SES identity→SNS bounce/complaint/delivery topic)
	EdgeValidatedBy  EdgeType = "VALIDATED_BY"  // certificate → domain-validation target (ACM cert→Route53 zone)
	EdgeAccessedBy   EdgeType = "ACCESSED_BY"   // scope → principal granted access (Azure Key Vault→AD principal via access policy OR RBAC role assignment)
	EdgeStoredIn     EdgeType = "STORED_IN"     // consumer → secret/cert container (Azure App Service cert→Key Vault resource)
	EdgeFromSnapshot EdgeType = "FROM_SNAPSHOT" // disk → source snapshot lineage (GCP disk→snapshot)
	EdgeFromImage    EdgeType = "FROM_IMAGE"    // disk → source image lineage (GCP disk→image)
	EdgeProxiesFrom  EdgeType = "PROXIES_FROM"  // registry virtual repo → upstream remote (GCP Artifact Registry pull-through)
	EdgeBackedUpBy   EdgeType = "BACKED_UP_BY"  // resource → backup/recovery target (DynamoDB table→PITR proxy or on-demand backup ARN; GCP Firestore DB→backup schedule)
	EdgeReplicatesTo EdgeType = "REPLICATES_TO" // primary → replica (S3 CRR/SRR destination bucket; RDS read replica; cross-region DR pairs)

	// CI/CD edge types (uppercase, from workflow/pipeline analysis).
	EdgeDeploysTo        EdgeType = "DEPLOYS_TO"        // workflow/pipeline → deploy target (environment, cluster, server)
	EdgeRunsIn           EdgeType = "RUNS_IN"           // workflow run → runner (self-hosted, GitHub-hosted, shared)
	EdgeRequiresApproval EdgeType = "REQUIRES_APPROVAL" // environment → reviewer/protection rule
	EdgeUsesSecret       EdgeType = "USES_SECRET"       // workflow → secret (name-only reference, no values)
	EdgeFederates        EdgeType = "FEDERATES"         // OIDC trust from CI/CD workflow → cloud IAM role
	EdgeBelongsTo        EdgeType = "BELONGS_TO"        // child resource → parent (project → group, environment → project, repo → org)
	EdgeHasLabel         EdgeType = "HAS_LABEL"         // resource → label/tag (runner → tag; unified name across providers)
	EdgeTriggeredBy      EdgeType = "TRIGGERED_BY"      // workflow run → trigger event (push, pull_request, schedule)

	// Cross-domain edge types (uppercase, for linkage graph relationships).
	EdgeBuilds     EdgeType = "BUILDS"     // Dockerfile/CI pipeline → container image → Deployment (code artifact produces cloud resource)
	EdgeDeploys    EdgeType = "DEPLOYS"    // Helm chart/deploy script → K8s resources (deployment tool creates cloud resources)
	EdgeManages    EdgeType = "MANAGES"    // IaC/SDK wrapper code → cloud resources (code manages cloud lifecycle)
	EdgeConfigures EdgeType = "CONFIGURES" // Config files in code → ConfigMaps/Secrets in cloud (code configures cloud resources)
	EdgeServes     EdgeType = "SERVES"     // Service/Ingress cloud resource → code endpoint (cloud routes traffic to code)

	// Log graph edge types (uppercase, emitted by the logs pipeline — see
	// GraphLogs in db.go and SkipsLLMProcessing for the exclusion rules).
	//
	// EdgeCorrelatesWith links two LogTemplate nodes whose error bursts
	// co-occurred in time AND whose owning services have a confirmed cloud
	// graph dependency (EdgeCalls, EdgeRoutesTo, etc.). Temporal-only
	// correlations without structural confirmation are surfaced in the
	// pipeline summary text instead of becoming an edge — per the
	// correlation design decision, edges are reserved for high-confidence
	// (temporal AND structural) pairs only.
	//
	// EdgeEmittedBy links a shared label node (service=..., namespace=...,
	// k8s_pod=...) to a cloud graph proxy resource that produced the logs.
	// This gives the log graph its only outbound reference into the wider
	// knowledge universe: the summary generator and future log tools
	// traverse EMITTED_BY to surface cloud topology alongside log patterns.
	EdgeCorrelatesWith EdgeType = "CORRELATES_WITH" // log-template → log-template (temporal + structural)
	EdgeEmittedBy      EdgeType = "EMITTED_BY"      // log label node → cloud graph proxy resource
)
