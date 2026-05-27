// SPDX-License-Identifier: Apache-2.0

// Package stackdriver implements a logwire.Provider for Google Cloud Logging
// (formerly Stackdriver).
//
// It self-registers via init() so importing this package is enough to make
// "stackdriver" available through logwire.New("stackdriver"). One provider
// instance serves a single GCP project; multi-project queries are not
// supported in v1 — configure one log_backend per project.
package stackdriver

import (
	"context"
	"fmt"
	"sync"

	"cloud.google.com/go/logging/logadmin"
	"google.golang.org/api/option"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func init() {
	logwire.Register("stackdriver", func() logwire.Provider { return &stackdriverProvider{} })
}

// stackdriverProvider implements logwire.Provider for GCP Cloud Logging.
// Authentication supports three modes, in priority order:
//  1. credentials_json  — raw service account JSON content (env var resolved
//     at the tools layer under auth_type="service_account").
//  2. service_account_path — filesystem path to a service account JSON file.
//  3. ADC (Application Default Credentials) — falls through when neither of
//     the above is set (workload identity, gcloud auth login, GCE metadata).
//
// Impersonation (impersonate_service_account) is deliberately not supported
// in v1; Configure returns an error if that key is set so operators see the
// limitation explicitly rather than getting silently different behavior.
type stackdriverProvider struct {
	projectID string
	credsJSON string
	saPath    string

	mu     sync.Mutex
	client *logadmin.Client
}

// Configure applies provider-specific settings from the config map.
// Supported keys:
//   - project_id           (required): GCP project to query. Also accepted
//     via the "url" key so operators can configure Stackdriver through
//     manage(configure_log_backend) — for GCP the "backend URL" is
//     semantically the project identifier.
//   - credentials_json     (optional): raw service account JSON content.
//     Also read from the universal "credential" key when auth_type is
//     "service_account" at the tools layer.
//   - service_account_path (optional): filesystem path to a service account
//     JSON file. Used only when credentials_json is empty.
//
// The "impersonate_service_account" key is explicitly rejected; v1 only
// supports direct credentials or ADC.
func (p *stackdriverProvider) Configure(cfg map[string]string) error {
	if _, ok := cfg["impersonate_service_account"]; ok && cfg["impersonate_service_account"] != "" {
		return fmt.Errorf("stackdriver: impersonate_service_account is not yet supported")
	}

	// Accept project_id explicitly, or fall back to the universal "url"
	// slot that manage(configure_log_backend) populates. This lets users
	// register Stackdriver backends through the standard tooling without
	// extending the log_backend schema.
	p.projectID = cfg["project_id"]
	if p.projectID == "" {
		p.projectID = cfg["url"]
	}
	if p.projectID == "" {
		return fmt.Errorf("stackdriver: project_id is required (set via 'project_id' or 'url' config key)")
	}

	// Resolution priority for credentials:
	//   1. service_account_path — explicit file path (auth_type=service_account_path)
	//   2. credentials_json     — explicit raw JSON content
	//   3. credential           — universal tools-layer slot, treated as raw JSON
	//                             ONLY when no service_account_path was provided
	//   4. ADC fallback — discovered by the GCP SDK from the environment
	p.saPath = cfg["service_account_path"]
	p.credsJSON = cfg["credentials_json"]
	if p.credsJSON == "" && p.saPath == "" {
		p.credsJSON = cfg["credential"]
	}

	// Reset the lazy client so the next Collect/ListSources rebuilds it
	// under the new credentials.
	p.mu.Lock()
	if p.client != nil {
		_ = p.client.Close()
	}
	p.client = nil
	p.mu.Unlock()
	return nil
}

// ensureClient lazily initializes the GCP logadmin client. The client is
// cached for the lifetime of the provider instance and torn down on
// Configure so credential rotations take effect on the next call.
func (p *stackdriverProvider) ensureClient(ctx context.Context) (*logadmin.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	if p.projectID == "" {
		return nil, fmt.Errorf("stackdriver: Configure must be called with project_id before use")
	}

	var opts []option.ClientOption
	switch {
	case p.credsJSON != "":
		opts = append(opts, option.WithCredentialsJSON([]byte(p.credsJSON)))
	case p.saPath != "":
		opts = append(opts, option.WithCredentialsFile(p.saPath))
	default:
		// ADC — the SDK discovers credentials from the environment
		// (GOOGLE_APPLICATION_CREDENTIALS, gcloud auth, metadata server).
	}

	client, err := logadmin.NewClient(ctx, p.projectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("stackdriver: create logadmin client: %w", err)
	}
	p.client = client
	return p.client, nil
}
