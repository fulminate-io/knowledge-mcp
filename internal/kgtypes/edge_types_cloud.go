// SPDX-License-Identifier: Apache-2.0

package kgtypes

// Cloud, CI/CD, cross-domain and log-graph edge types.
//
// THEY LIVE BESIDE edge_types.go RATHER THAN IN IT, and the split is by
// VOCABULARY rather than by size alone: that file holds the CODE and KNOWLEDGE
// edge types, which the collector and the reasoning graph produce, while every
// constant here is produced by a cloud, CI/CD, linkage or logs collector. One
// file holding all five was a grab-bag that crossed the 500-line cap the moment
// any one vocabulary grew.
const (
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
