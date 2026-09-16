// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"os"
	"regexp"
)

// CredentialNamespaceEnv names the environment variable that selects the
// credential NAMESPACE this process reads and writes: the keychain service
// becomes ServiceName+"."+namespace and the file-store fallback keys its file
// by the same name, so a process under a namespace shares no credential with
// the default one.
//
// It exists for a trusted-development launch — a Desktop build signing in to
// a development backend on the operator's own machine — which must not
// overwrite the login the host CLI uses. It is a TEST-MODE OVERRIDE, never a
// product default: unset, every store this package opens behaves exactly as
// it did before the selector existed.
//
// It is the namespace half of the same rule [CredentialStoreReadOnlyEnv]
// serves and is deliberately independent of it. Read-only says "do not write
// the operator's credentials"; the namespace says "write somewhere else".
// A process may reasonably want either alone.
const CredentialNamespaceEnv = "KNOWLEDGE_CREDENTIAL_NAMESPACE"

// credentialNamespaceShape is the accepted shape, kept as its own constant
// because the refusal message quotes it: an operator who mistyped the value
// should not have to find the documentation to learn what was expected.
const credentialNamespaceShape = `^[a-z0-9][a-z0-9-]{0,31}$`

// credentialNamespacePattern is the compiled shape gate, package-level so the
// compile happens once at init rather than per store open.
//
// Lowercase-only, dash-separated and length-bounded, because the value becomes
// part of a keychain service identifier and a filename on three platforms. The
// exclusion of "." is load-bearing rather than cosmetic: the derived service
// name is ServiceName+"."+namespace, so a namespace carrying a dot could name
// a service another namespace also derives.
var credentialNamespacePattern = regexp.MustCompile(credentialNamespaceShape)

// ErrCredentialNamespaceInvalid is the sentinel every caller branches on.
//
// It is exported because four call sites must tell a malformed selector from
// every other credential-store failure and act differently: the six Desktop
// verbs return a hard error instead of their signed-out JSON, `doctor` renders
// the refusal as a configuration error rather than the fully-supported
// not-logged-in state, and `serve` refuses to start. Those branches compare
// with errors.Is; none of them may match on the message, which is prose and
// will be reworded.
var ErrCredentialNamespaceInvalid = errors.New("auth: not a credential namespace")

// CredentialNamespace resolves the credential namespace for this process.
//
// An UNSET variable is an absence, not a selection: it returns the empty
// namespace and no error, which every derivation in this package reads as
// "the default". A SET value that does not match the accepted shape is an
// ERROR naming the variable, the offending value and the shape — never a
// silent fall back to the default namespace, because a process that asked for
// isolation and silently got the operator's credentials is the failure this
// variable exists to prevent. Empty-when-set is therefore a refusal and not an
// absence, which is why this reads os.LookupEnv rather than os.Getenv.
//
// It is STORE-FREE: it opens no keychain and touches no file, so a caller can
// refuse an invalid value before doing any work. [OpenStore] calls it too, so
// a caller that skips it is refused downstream rather than silently served;
// callers must not re-derive the value from the environment themselves, since
// one predicate is what keeps the accepted shape from drifting between the
// check and the enforcement.
func CredentialNamespace() (string, error) {
	raw, ok := os.LookupEnv(CredentialNamespaceEnv)
	if !ok {
		return "", nil
	}
	if !credentialNamespacePattern.MatchString(raw) {
		return "", fmt.Errorf("%w: %s=%q; accepted shape is %s",
			ErrCredentialNamespaceInvalid, CredentialNamespaceEnv, raw, credentialNamespaceShape)
	}
	return raw, nil
}

// credentialServiceName derives the keychain "service" identifier for a
// namespace. The empty namespace is the default and yields [ServiceName]
// unchanged, byte for byte — the property that makes an unset selector
// indistinguishable from the behavior before the selector existed.
//
// The derived name is computed ONCE, at store open, and carried on the store.
// Re-deriving it at each backend call site would be a place the derivation
// could differ between two calls of one process.
func credentialServiceName(namespace string) string {
	if namespace == "" {
		return ServiceName
	}
	return ServiceName + "." + namespace
}

// credentialsFileNameFor derives the file-store basename for a namespace, on
// the same terms: the empty namespace keeps the historical "credentials" file
// untouched, and a namespace gets a sibling of its own so two namespaces never
// share a credentials file when the keychain is unavailable.
func credentialsFileNameFor(namespace string) string {
	if namespace == "" {
		return credentialsFileName
	}
	return credentialsFileName + "." + namespace
}
