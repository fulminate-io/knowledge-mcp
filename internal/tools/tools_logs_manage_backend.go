// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// handleConfigureLogBackend creates or updates a NodeLogBackend record in
// the knowledge graph. The record captures how to reach a log provider
// (CloudWatch, Loki, Stackdriver, ...) including its credential, which is
// stored encrypted at rest (AES-256-GCM with a machine-bound key, same as
// every other graph blob). Storing the raw value simplifies LLM workflows:
// no external env var plumbing is required.
//
// Client-side handler. Reads/writes go through gc.Call("query",
// ...) and gc.Call("mutate", operation:"upsert", ...) against the server.
// Server-side handleConfigureLogBackend is gone — the dispatch returns
// errLogsHandledClientSide so older clients can detect the move.
//
// Contract:
//   - name         : stable identifier and primary key. Immutable after first
//     write — uniqueness is enforced via deterministic node ID
//     so concurrent creates collapse to a single upsert.
//   - provider     : cloudwatch | loki | stackdriver | k8s | ... Immutable.
//   - kube_context : the kubeconfig context name for k8s backends. Immutable
//     once set — silent rebinds to a different cluster would
//     corrupt query_id namespacing and stale on-disk log
//     graphs. Operators retargeting must register under a new
//     name.
//   - url          : base URL or project identifier. Mutable for non-k8s
//     backends; for k8s where url is the kube_context
//     fallback, immutable.
//   - auth_type    : Mutable.
//   - credential   : raw credential value or a $ENV_VAR reference. Mutable.
func (h *Handler) handleConfigureLogBackend(ctx context.Context, a manageArgs) kgtools.ToolResult {
	if err := validateConfigureArgs(a); err != nil {
		return kgtools.ErrorResult(err.Error())
	}
	gc := h.graphCaller()
	if gc == nil {
		return kgtools.ErrorResult("configure_log_backend: no GraphCaller configured")
	}
	existing, found, err := findLogBackendByName(ctx, gc, a.Name)
	if err != nil {
		return kgtools.ErrorResult("configure_log_backend: " + err.Error())
	}
	if found {
		if err := validateImmutable(existing, a); err != nil {
			return kgtools.ErrorResult(err.Error())
		}
		return upsertLogBackend(ctx, gc, existing.id, a, false)
	}
	// Use the operator-supplied name AS the node ID. The name is already
	// the unique identifier callers reference; making it the ID gives us
	// O(1) ByID lookup and lets db.Upsert enforce uniqueness implicitly
	// (concurrent creates collapse to one upsert instead of producing two
	// UUID-keyed nodes that share a SymbolName).
	return upsertLogBackend(ctx, gc, strings.TrimSpace(a.Name), a, true)
}

// logBackendRecord is the minimal view of a log_backend node the
// client-side handlers need. Mirrors the shape returned by query(type=
// log-backend, format=json): id (== name), symbol_name (display name),
// and the configuration metadata fields.
type logBackendRecord struct {
	id         string
	symbolName string
	provider   string
	url        string
	authType   string
	credential string
	kubeCtx    string
}

// value returns the stored metadata-style accessor used by validateImmutable.
// Mirrors *knowledgev1.Node.Value so the legacy callers read the same shape.
func (r logBackendRecord) value(key string) string {
	switch key {
	case "provider":
		return r.provider
	case "url":
		return r.url
	case "auth_type":
		return r.authType
	case "credential":
		return r.credential
	case "kube_context":
		return r.kubeCtx
	}
	return ""
}

// validateImmutable blocks changes to provider, kube_context, and url on
// an existing record. These are identity-bearing fields used by the
// query_id derivation and downstream resolvers; silently rotating them
// would corrupt log-graph namespacing (two clusters sharing the same
// query_id) and orphan on-disk graphs.
func validateImmutable(existing logBackendRecord, a manageArgs) error {
	if existing.value("provider") != a.Provider {
		return fmt.Errorf(
			"configure_log_backend: provider is immutable (stored=%q, requested=%q) — "+
				"create a new backend under a different name instead",
			existing.value("provider"), a.Provider,
		)
	}
	// k8s backends key on cluster identity (kube_context, with url as the
	// pre-Phase-4 fallback). Either field changing would re-target the
	// backend at a different cluster — refuse, since the operator likely
	// wants a NEW backend rather than to silently rebind an existing one.
	if existing.value("provider") == providerK8s {
		stored := backendK8sIdentity(existing)
		requested := strings.TrimSpace(a.KubeContext)
		if requested == "" {
			requested = strings.TrimSpace(a.URL)
		}
		if stored != "" && requested != "" && stored != requested {
			return fmt.Errorf(
				"configure_log_backend: kube_context is immutable for k8s backends "+
					"(stored=%q, requested=%q) — create a new backend under a different name to target a new cluster",
				stored, requested,
			)
		}
	}
	return nil
}

// backendK8sIdentity returns the cluster identity stored on a k8s backend
// record, preferring kube_context over url (matching the provider's
// Configure() resolution order).
func backendK8sIdentity(r logBackendRecord) string {
	if v := strings.TrimSpace(r.value("kube_context")); v != "" {
		return v
	}
	return strings.TrimSpace(r.value("url"))
}

// findLogBackendByName resolves a NodeLogBackend record by its name from the
// type-browse of every configured backend, matched client-side. Returns (record,
// true, nil) when found, (zero, false, nil) when absent, (zero, false,
// err) on transport failure.
//
// The match is client-side, so the browse must return the COMPLETE backend set:
// it drains keyset pages rather than taking one bounded page, or a user with
// more than a page of backends would silently lose the tail.
func findLogBackendByName(ctx context.Context, gc GraphCaller, name string) (logBackendRecord, bool, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return logBackendRecord{}, false, nil
	}
	nodes, err := drainQueryNodes(ctx, gc, map[string]any{
		"type": string(kgtypes.NodeLogBackend),
	})
	if err != nil {
		return logBackendRecord{}, false, fmt.Errorf("query log_backends: %w", err)
	}
	records := logBackendRecordsFromNodes(nodes)
	for _, r := range records {
		if r.symbolName == trimmed || r.id == trimmed {
			return r, true, nil
		}
	}
	return logBackendRecord{}, false, nil
}

// logBackendRecordsFromNodes builds logBackendRecords from the drained
// log-backend type-browse nodes. The log-backend nodes are knowledge-graph
// records; SymbolName is the name, the provider/url/auth_type/credential/
// kube_context fields ride node Metadata.
func logBackendRecordsFromNodes(nodes []*knowledgev1.Node) []logBackendRecord {
	out := make([]logBackendRecord, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, logBackendRecord{
			id:         n.Id,
			symbolName: n.SymbolName,
			provider:   kgtypes.Value(n, "provider"),
			url:        kgtypes.Value(n, "url"),
			authType:   kgtypes.Value(n, "auth_type"),
			credential: kgtypes.Value(n, "credential"),
			kubeCtx:    kgtypes.Value(n, "kube_context"),
		})
	}
	return out
}

// upsertLogBackend writes the log_backend node via gc.Call("mutate",
// operation:"upsert", type:"log-backend", ...). isNew toggles the
// "created" vs "updated" verb on the success message; both code paths
// hit the same upsert RPC because the server's mutate(upsert) is
// create-or-update by id.
func upsertLogBackend(ctx context.Context, gc GraphCaller, id string, a manageArgs, isNew bool) kgtools.ToolResult {
	meta := map[string]string{
		"provider":   a.Provider,
		"url":        a.URL,
		"auth_type":  a.AuthType,
		"credential": a.Credential,
	}
	if kc := strings.TrimSpace(a.KubeContext); kc != "" {
		// Context names are non-sensitive and stored as plain metadata so
		// listing surfaces them verbatim. Omitted when empty to keep the
		// map compact for classical bearer/basic backends.
		meta["kube_context"] = kc
	}
	payload := map[string]any{
		"operation":   "upsert",
		"type":        "log-backend",
		"id":          id,
		"name":        a.Name,
		"source":      "manage:configure_log_backend",
		"description": fmt.Sprintf("Log backend: provider=%s auth=%s", a.Provider, a.AuthType),
		"metadata":    meta,
	}
	args, err := json.Marshal(payload)
	if err != nil {
		return kgtools.ErrorResult("upsert log_backend marshal: " + err.Error())
	}
	if _, err := executeMutate(ctx, gc, args); err != nil {
		return kgtools.ErrorResult("upsert log_backend: " + err.Error())
	}
	verb := "updated"
	if isNew {
		verb = "created"
	}
	return kgtools.TextResult(formatConfigureSuccess(a, verb))
}

// formatConfigureSuccess renders the operator-facing confirmation message.
// K8s backends surface kube_context instead of a redacted credential so the
// one piece of identifying information they do carry shows up at a glance.
func formatConfigureSuccess(a manageArgs, verb string) string {
	if strings.TrimSpace(a.KubeContext) != "" {
		return fmt.Sprintf(
			"log_backend %q %s (provider=%s auth_type=%s kube_context=%s)",
			a.Name, verb, a.Provider, a.AuthType, a.KubeContext,
		)
	}
	return fmt.Sprintf(
		"log_backend %q %s (provider=%s auth_type=%s credential=%s)",
		a.Name, verb, a.Provider, a.AuthType, redactCredential(a.Credential),
	)
}

// redactCredential returns a short preview safe to echo back in tool
// responses. $ENV_VAR references render as-is (they contain no secret), but
// raw values are redacted so the secret doesn't reappear in the caller's
// conversation context.
func redactCredential(v string) string {
	trimmed := strings.TrimSpace(v)
	if strings.HasPrefix(trimmed, "$") {
		return trimmed
	}
	if len(trimmed) <= 4 {
		return "[redacted]"
	}
	return "[redacted " + fmt.Sprintf("%d", len(trimmed)) + " chars]"
}

// handleListLogBackends returns all configured log_backend records sorted
// by name. The response never includes resolved credential values — only
// the env var reference, so audit callers can see what would be loaded at
// query time without leaking any secret material.
//
// Client-side handler. The list is drained from a log-backend type-browse over
// the Execute carrier seam and rendered locally. "All configured records" means
// every one of them, so the browse drains keyset pages rather than taking one
// bounded page.
func (h *Handler) handleListLogBackends(ctx context.Context, format string) kgtools.ToolResult {
	gc := h.graphCaller()
	if gc == nil {
		return kgtools.ErrorResult("list_log_backends: no GraphCaller configured")
	}
	nodes, err := drainQueryNodes(ctx, gc, map[string]any{
		"type": string(kgtypes.NodeLogBackend),
	})
	if err != nil {
		return kgtools.ErrorResult("list log_backends: " + err.Error())
	}
	records := logBackendRecordsFromNodes(nodes)

	sort.Slice(records, func(i, j int) bool {
		return records[i].symbolName < records[j].symbolName
	})

	if format == "json" {
		return jsonResult(logBackendsAsJSON(records))
	}
	return kgtools.TextResult(formatLogBackendsTable(records))
}

// logBackendsAsJSON returns a compact marshaling-friendly view. Credential
// values are redacted ($ENV_VAR references render as-is since they carry no
// secret; raw values are replaced with a length-only placeholder).
// kube_context is surfaced verbatim — context names are non-sensitive and
// identify which cluster a k8s backend targets.
func logBackendsAsJSON(records []logBackendRecord) []map[string]string {
	out := make([]map[string]string, 0, len(records))
	for _, r := range records {
		out = append(out, map[string]string{
			"name":         r.symbolName,
			"provider":     r.value("provider"),
			"url":          r.value("url"),
			"auth_type":    r.value("auth_type"),
			"credential":   redactCredential(r.value("credential")),
			"kube_context": r.value("kube_context"),
		})
	}
	return out
}

// formatLogBackendsTable renders the list as a markdown table. The empty
// case returns a short helpful message so callers can distinguish "no
// backends configured yet" from "list failed". kube_context surfaces in
// its own column so k8s backends show which cluster context they target
// without muddying the credential column (which stays empty for kubeconfig
// backends).
func formatLogBackendsTable(records []logBackendRecord) string {
	if len(records) == 0 {
		return "No log_backend records configured. Use manage(operation: \"configure_log_backend\", ...) to register one."
	}
	var sb strings.Builder
	sb.WriteString("| name | provider | url | auth_type | credential | kube_context |\n")
	sb.WriteString("|------|----------|-----|-----------|------------|--------------|\n")
	for _, r := range records {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s |\n",
			r.symbolName,
			r.value("provider"),
			r.value("url"),
			r.value("auth_type"),
			formatBackendCredentialCell(r),
			orDash(r.value("kube_context")),
		)
	}
	return sb.String()
}

// formatBackendCredentialCell renders the credential column: k8s/kubeconfig
// rows have no credential to show (auth is env-resolved), so they render as
// a dash for visual parity with empty metadata.
func formatBackendCredentialCell(r logBackendRecord) string {
	cred := r.value("credential")
	if cred == "" {
		return "-"
	}
	return redactCredential(cred)
}

// orDash returns "-" when s is empty, otherwise s. Keeps markdown table
// cells visually consistent when a column is optional across rows.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
