// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// authKubeconfig is the auth_type identifier for the k8s provider. The
// k8s provider does not consume an embedded credential — auth is resolved
// by client-go from the operator's environment (KUBECONFIG,
// ~/.kube/config, or in-cluster service account). Callers must instead
// supply a kube_context so the provider knows which cluster to target.
const authKubeconfig = "kubeconfig"

// providerK8s is the registered logwire.Provider name for Kubernetes Events.
// Kept as a package constant so validation checks and help text use the
// same spelling.
const providerK8s = "k8s"

// validateConfigureArgs enforces the field matrix for configure_log_backend.
//
// Two shapes are legal:
//
//   - Standard backends (cloudwatch, loki, stackdriver, ...): require
//     name + provider + url + auth_type + credential. The credential is
//     encrypted at rest or resolved from a $ENV_VAR reference at query time.
//   - K8s Events (provider=k8s, auth_type=kubeconfig): require name +
//     provider + url + kube_context. Credential is optional — client-go
//     resolves auth from the operator's environment, so there is no secret
//     for the knowledge graph to hold.
//
// The switch-on-auth-type layout keeps future auth modes (e.g., k8s
// in-cluster service account) easy to add without reshaping the common
// required-field checks.
func validateConfigureArgs(a manageArgs) error {
	if err := validateCommonBackendFields(a); err != nil {
		return err
	}
	authType := strings.ToLower(strings.TrimSpace(a.AuthType))
	provider := strings.ToLower(strings.TrimSpace(a.Provider))

	if provider == providerK8s && authType != authKubeconfig {
		return fmt.Errorf(
			"configure_log_backend: provider=%q requires auth_type=%q (got %q)",
			providerK8s, authKubeconfig, a.AuthType,
		)
	}

	switch authType {
	case authKubeconfig:
		return validateKubeconfigAuth(a)
	default:
		return validateCredentialAuth(a)
	}
}

// validateCommonBackendFields checks the fields every configure call must
// set regardless of auth mode. The auth-mode-specific checks layer on top.
func validateCommonBackendFields(a manageArgs) error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("configure_log_backend: name is required")
	}
	if strings.TrimSpace(a.Provider) == "" {
		return fmt.Errorf("configure_log_backend: provider is required")
	}
	// Reject unknown providers at config time. Without this check the
	// configure path silently persists a backend whose first use will
	// fail with "unknown provider" — the caller never learns the typo
	// existed until they trigger a collect / list_logs call.
	//
	// Skip when the registry is empty: this is the in-process test/
	// library configuration where the cmd/knowledge-server binary's
	// init-time provider imports haven't run. Production server boot
	// always seeds the registry, so the empty case at runtime means
	// "we're in a unit test" — leaving validation as a no-op there
	// keeps test plumbing simple.
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if registered := logwire.Available(); len(registered) > 0 && !logwire.IsRegistered(provider) {
		return fmt.Errorf("configure_log_backend: unknown provider %q (registered: %v)", a.Provider, registered)
	}
	if strings.TrimSpace(a.URL) == "" {
		return fmt.Errorf("configure_log_backend: url is required")
	}
	if strings.TrimSpace(a.AuthType) == "" {
		return fmt.Errorf("configure_log_backend: auth_type is required")
	}
	return nil
}

// validateKubeconfigAuth enforces the k8s-via-kubeconfig shape: the
// kube_context is mandatory (it's the only way to tell the provider which
// cluster to talk to) and credential is explicitly optional.
func validateKubeconfigAuth(a manageArgs) error {
	if strings.TrimSpace(a.KubeContext) == "" {
		return fmt.Errorf(
			"configure_log_backend: auth_type=%q requires kube_context (kubeconfig context name)",
			authKubeconfig,
		)
	}
	return nil
}

// validateCredentialAuth enforces the classical bearer/basic/api_key
// shape: the credential field is mandatory so downstream providers can
// read it from the config map.
func validateCredentialAuth(a manageArgs) error {
	if strings.TrimSpace(a.Credential) == "" {
		return fmt.Errorf("configure_log_backend: credential is required")
	}
	return nil
}
