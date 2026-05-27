// SPDX-License-Identifier: Apache-2.0

// Package tools — help topic for the logs graph and tooling.
//
// The logs topic is split out of tools_help_content.go / tools_help_content2.go
// so neither file creeps over the 500-line hard cap, and so the log-graph
// documentation stays co-located with the other log tooling files.
package tools

const helpLogs = `# Logs — ephemeral log graphs

Most log-graph 'query' modes ('pivot', 'correlations', 'timeline', 'explain') are
generic primitives — they work on any graph; defaults shown here are logs
conventions. 'resolver' is logs-specific.

Log graphs are ephemeral Knowledge graphs seeded from an external log backend
(CloudWatch, Loki, Elasticsearch, Stackdriver, K8s Events, ...). Each
collection run materializes a fresh graph keyed by a deterministic query_id
under ~/.knowledge/logs/.

Key properties:
  - Ephemeral: graphs are discarded on demand via manage discard_logs.
  - LLM-excluded: log graph nodes are skipped by the summarizer and embedder
    pipelines (see store.SkipsLLMProcessing), so no Claude/Voyage traffic leaks
    potentially sensitive log content. BM25 remains available because the graph
    layer always builds a text index.
  - One graph per query: the query_id encodes (backend, source, time range,
    filters) so reruns target the same on-disk graph and incremental additions
    are possible without re-collecting from scratch.
  - Cloud-correlated: log labels that match cloud resource identifiers (ARNs,
    Kubernetes names, etc.) get EMITTED_BY edges to the cloud graph's proxy
    nodes. Traversing a stream surfaces those cross-graph links. Cloud-graph
    selection is auto-discovered per stream from labels (project_id,
    cluster_name, ...). For GCP/GKE the resolver targets the matching
    "gke_{project_id}_*_{cluster_name}" graph first, falling back to the
    parent "{project_id}" graph when no cluster graph matches. Other
    providers fall back to scanning every loaded cloud graph. There is no
    cloud_account parameter — the operator simply ensures the relevant
    cloud graphs are indexed.

## 1. Configure a backend (once)
  manage({ "operation": "configure_log_backend",
           "name": "prod-loki",
           "provider": "loki",
           "url": "https://loki.example.com",
           "auth_type": "bearer",
           "credential": "eyJhbGciOi..." })          // raw bearer token
  // or
  manage({ "operation": "configure_log_backend",
           "name": "prod-loki",
           "provider": "loki",
           "url": "https://loki.example.com",
           "auth_type": "bearer",
           "credential": "$LOKI_TOKEN" })             // env var ref

  credential accepts either the raw secret value OR a $ENV_VAR reference
  resolved at query time. Raw values are stored encrypted at rest (AES-256-GCM,
  machine-bound key) and redacted in tool responses.

### Provider: k8s (Kubernetes Events)

  manage({ "operation": "configure_log_backend",
           "name": "k8s-prod",
           "provider": "k8s",
           "url": "gke_myproject_us-central1_prod",
           "auth_type": "kubeconfig",
           "kube_context": "gke_myproject_us-central1_prod" })

  The k8s provider surfaces cluster Events (events.k8s.io/v1) as log entries.
  It authenticates via client-go's standard kubeconfig loader using the
  operator's environment — kubeconfig context auth plugins (gcloud,
  aws-iam-authenticator, in-cluster service account, ...) are honored. No
  credential material transits the knowledge graph: auth_type="kubeconfig"
  accepts an empty credential, and kube_context identifies the target cluster
  from ~/.kube/config.

  url is still required — set it to the kubecontext name (or any stable
  cluster identifier) for parity with other backends in list_log_backends
  output. kube_context is the authoritative identifier consumed by the
  provider.

## 2. Collect a query window
  collect({ "type": "logs",
            "backend": "prod-loki",
            "source": "{app=\"api\"} |= \"ERROR\"",
            "start": "2026-04-13T00:00:00Z",
            "end":   "2026-04-13T02:00:00Z",
            "severity_min": "WARN",
            "max_entries": 5000 })

  # Same call shape for k8s — the backend lookup carries provider + context:
  collect({ "type": "logs",
            "backend": "k8s-prod",
            "start": "2026-04-13T00:00:00Z",
            "end":   "2026-04-13T02:00:00Z",
            "severity_min": "WARN" })

  The handler returns a query_id + summary (template/stream/chunk counts,
  correlation count). The graph persists under ~/.knowledge/logs/<query_id>.bin.

## 3. Explore via query (overview → drill-down → detail)
  query({ "graph": "logs", "name": "<query_id>" })
    — label overview table ranked by error count.
  query({ "graph": "logs", "name": "<query_id>",
          "text": "app=api severity>=WARN" })
    — intersected label + severity drill-down: matching streams and the
      templates they reference.
  query({ "graph": "logs", "name": "<query_id>", "id": "<template_id>" })
    — template detail: pattern, severity, affected labels, and decompressed
      example entries.

  Drill-down grammar (AND-only MVP):
    expr      = filter (WS filter)*
    filter    = key=value | severity(=|>=|>)LEVEL
    LEVEL     = TRACE | DEBUG | INFO | WARN | ERROR | CRITICAL

## 4. Search templates via BM25
  search({ "graph": "logs", "name": "<query_id>",
           "query": "connection refused" })

  Results are filtered to log-template nodes — chunks hold compressed payloads
  and streams are label buckets, so neither is useful as a plain search hit.

## 5. Traverse templates/streams
  traverse({ "graph": "logs", "name": "<query_id>",
             "start": "<template_id>", "direction": "out" })
    — template → chunks (contains) + a short decoded entry sample per chunk.

  traverse({ "graph": "logs", "name": "<query_id>",
             "start": "<stream_id>",  "direction": "both" })
    — stream → shared labels (has_label) + chunks (belongs_to) + cloud proxies
      reachable from each label (emitted_by).

## Stream and template aliases

Every stream and template carries a readable alias derived from its
labels (streams) or its pattern + severity (templates). Aliases appear
alongside the raw SHA-256 hash everywhere a log object is rendered, and
are accepted as input wherever a hex ID is accepted.

  Stream alias examples (per-provider):
    K8s events:  api-7b6.OOMKilled            (<pod_or_object>.<reason>)
    Loki:        checkout@host-3              (<app>@<instance>)
    Stackdriver: api-7b6.k8s_container        (<host>.<resource_type>)
    CloudWatch:  ecs-task-1234.api-server     (<log_stream>.<service>)
    Generic:     account=1234.env=prod        (first two labels by key)

  Template alias examples (kebab-case body + @<sev-short>):
    "Node <*> is not ready" / WARN  → node-not-ready@warn
    "OOMKilled container <*>" / ERR → oomkilled-container@err

Aliases are case-preserving — OOMKilled stays OOMKilled. Lookups are
case-insensitive, so api-7b6.oomkilled and API-7b6.OOMKILLED resolve to
the same stream. When two streams or two templates would share an alias,
the second occurrence (sorted by ID for determinism) is suffixed with a
short hash: api.OOMKilled@a1b2c3d4 for streams; pod-evicted@warn-a1b2
for templates.

You can use either form anywhere a hex ID is expected:
  query({ graph: "logs", name: "<qid>", id: "node-not-ready@warn" })
  traverse({ graph: "logs", name: "<qid>", start: "api-7b6.OOMKilled", direction: "both" })

## 6. Discard when done
  manage({ "operation": "discard_logs", "name": "<query_id>" })  — drop one graph
  manage({ "operation": "discard_logs" })                          — drop all

## Example LLM workflow

  # 1. A user reports "the API is returning 500s around 10am"
  manage list_log_backends                              # find which backend to use
  collect type=logs backend=prod-loki \
          source='{app="api"}' \
          start=2026-04-13T09:30:00Z end=2026-04-13T10:30:00Z \
          severity_min=ERROR                            # returns query_id=qid123
  query graph=logs name=qid123                          # see error-heavy labels
  query graph=logs name=qid123 text='pod=api-7b6 severity>=ERROR'  # drill into the suspicious pod
  search graph=logs name=qid123 query='database timeout'           # BM25 for the exact pattern
  traverse graph=logs name=qid123 start=<stream_id> direction=both # cloud-resource context

  # When done
  manage discard_logs name=qid123
`
