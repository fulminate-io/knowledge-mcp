// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// desktopCredentialNamespaceEnv is the knowledge client's credential-namespace
// selector, named here because this file OBSERVES it rather than honors it:
// the Desktop forwards it to every process it spawns, and these fixtures are
// what proves the forwarding reached a real child.
const desktopCredentialNamespaceEnv = "KNOWLEDGE_CREDENTIAL_NAMESPACE"

// desktopSpawnNamespaceKey is the fixture-store key the report below is
// written under. The Electron auth driver's store fixture accepts any key and
// the driver reads this one back; it is a test observation and no production
// path reads or writes it.
const desktopSpawnNamespaceKey = "desktop-spawn-credential-namespace"

// reportSpawnedCredentialNamespace publishes the credential namespace this
// process was spawned with, VERBATIM, through the store the Electron fixture
// supplies. The empty string is a value here rather than an omission: a reader
// that saw no entry could not tell a Desktop that forwarded nothing from a
// Desktop that never spawned this binary at all.
//
// A NAMESPACE SELECTOR IS NOT A CREDENTIAL. It names where credentials are
// kept; it unlocks nothing, and this function reads no credential key.
func reportSpawnedCredentialNamespace(ctx context.Context, store auth.Store) error {
	return store.Set(ctx, desktopSpawnNamespaceKey, os.Getenv(desktopCredentialNamespaceEnv))
}

// TestDesktopAuthProcess is the existing CLI compiled as a test binary with
// external OAuth/browser/native-store dependencies supplied by the Electron
// fixture. No production credential store or cloud endpoint is contacted.
func TestDesktopAuthProcess(t *testing.T) {
	endpoint := os.Getenv("DESKTOP_AUTH_FIXTURE")
	if endpoint == "" {
		t.Skip("Electron auth fixture absent")
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) < 2 || (args[0] != "desktop-auth" && args[0] != "desktop-remote") {
		t.Fatal("missing desktop action")
	}
	store := desktopHTTPStore{endpoint: endpoint}
	// WHAT THIS PROCESS WAS SPAWNED WITH, reported before anything is
	// dispatched. The Desktop forwards the credential-namespace selector
	// through an environment allowlist, and an allowlist is a property of the
	// PARENT: nothing the Desktop's own unit rows can observe distinguishes a
	// selector that reached a real child from one that was only meant to. This
	// is the observation that does, and it is reflection on this process's own
	// environment rather than any behaviour of the selector — a client that
	// ignores the variable entirely still reports it correctly here.
	if err := reportSpawnedCredentialNamespace(t.Context(), store); err != nil {
		t.Fatalf("reportSpawnedCredentialNamespace: got error %v, want nil", err)
	}
	newStoreFn = func() (auth.Store, error) { return store, nil }
	discoverFn = func(context.Context, string, map[string]struct{}) (*auth.DiscoveredEndpoints, error) {
		return &auth.DiscoveredEndpoints{AuthorizationEndpoint: endpoint + "/authorize", RegistrationEndpoint: endpoint + "/register", TokenEndpoint: endpoint + "/token", RevocationEndpoint: endpoint + "/revoke"}, nil
	}
	desktopBrowserFlow = func(ctx context.Context, e *auth.DiscoveredEndpoints) (string, *auth.TokenResponse, error) {
		return auth.RunBrowserPKCEFlowWithBrowser(ctx, e, func(target string) error {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				return err
			}
			r, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer r.Body.Close()
			if r.StatusCode >= 400 {
				return errors.New("fixture browser failed")
			}
			return nil
		})
	}
	base := http.DefaultTransport
	http.DefaultTransport = desktopFixtureTransport{base: base, endpoint: endpoint}
	desktopAccountTransport = func(store auth.Store, configPath string) *auth.Transport {
		return auth.NewSyncTransport(endpoint, auth.NewOAuthTokenSource(store, endpoint, map[string]struct{}{"fixture.invalid": {}}), auth.WithAccountSelection(auth.NewAccountSelection(configPath, time.Second)))
	}
	if args[0] == "desktop-remote" {
		if relay := os.Getenv("DESKTOP_DASHBOARD_RELAY"); relay != "" {
			dashboardRelayURL = func() (string, error) { return relay, nil }
		}
		nativeManagementTransport = func(store auth.Store) *auth.Transport {
			return auth.NewSyncTransport(os.Getenv("DESKTOP_MANAGEMENT_FIXTURE"), auth.NewOAuthTokenSource(store, endpoint, map[string]struct{}{"fixture.invalid": {}}))
		}
		if err := DesktopRemoteCmd(args[1:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if err := DesktopAuthCmd(args[1:]); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// TestReportSpawnedCredentialNamespace is the in-process half of the arm
// above: it proves the report is verbatim without an Electron fixture, so the
// arm has a red of its own outside the Desktop's venue. The Desktop drives the
// same helper through a real process boundary in its auth driver.
func TestReportSpawnedCredentialNamespace(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
		unset bool
		want  string
	}{
		{name: "a namespace the launcher set", value: "ns-1", want: "ns-1"},
		{name: "a namespace this process would refuse is still reported verbatim", value: "NOT VALID", want: "NOT VALID"},
		{name: "an unset selector", unset: true, want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(desktopCredentialNamespaceEnv, testCase.value)
			if testCase.unset {
				// t.Setenv above registered the restore, so unsetting here is
				// undone with the rest of the case.
				if err := os.Unsetenv(desktopCredentialNamespaceEnv); err != nil {
					t.Fatalf("os.Unsetenv(%q) = %v, want nil", desktopCredentialNamespaceEnv, err)
				}
			}
			store := newFakeAuthStore()
			if err := reportSpawnedCredentialNamespace(context.Background(), store); err != nil {
				t.Fatalf("reportSpawnedCredentialNamespace(%q) = %v, want nil", testCase.value, err)
			}
			got, err := store.Get(context.Background(), desktopSpawnNamespaceKey)
			if err != nil {
				// An unset selector must still be REPORTED, as the empty
				// string: a driver that could not tell "forwarded nothing"
				// from "wrote nothing" would pass on the forwarding being
				// removed outright.
				t.Fatalf("store.Get(%q) = %v, want the reported namespace", desktopSpawnNamespaceKey, err)
			}
			if got != testCase.want {
				t.Errorf("reportSpawnedCredentialNamespace(%q) reported %q, want %q", testCase.value, got, testCase.want)
			}
			// THE CONTROL: a namespace selector is not a credential, and this
			// helper touches none of the credential keys that are, which the
			// list below names.
			for _, key := range []string{auth.KeyClientID, auth.KeyRefreshToken, auth.KeyAccessToken, auth.KeyAccessTokenExpiry} {
				if _, err := store.Get(context.Background(), key); !errors.Is(err, auth.ErrNotFound) {
					t.Errorf("reportSpawnedCredentialNamespace(%q) wrote the credential key %q, want it untouched", testCase.value, key)
				}
			}
		})
	}
}

type desktopHTTPStore struct{ endpoint string }

// call reaches the Electron fixture's key/value store.
//
// THE ENDPOINT IS A FIXTURE THIS PROCESS WAS SPAWNED WITH, which is why the two
// gosec G704 sinks below are declared trusted rather than validated: s.endpoint
// is read from DESKTOP_AUTH_FIXTURE by TestDesktopAuthProcess, which SKIPS when
// the variable is unset, so the only host this ever reaches is the loopback
// server the Electron driver started for this test binary. No production path
// constructs a desktopHTTPStore. A scheme-and-host check here would gate a value
// the test itself supplies and would observe nothing the fixture cannot already
// choose; if this store ever took an endpoint from anywhere else, the nolints
// come off and the validation goes in.
func (s desktopHTTPStore) call(ctx context.Context, method, key, value string) (string, error) {
	body, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, method, s.endpoint+"/store/"+url.PathEscape(key), bytes.NewReader(body)) //nolint:gosec // G704: fixture endpoint, see the trust note above
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request) //nolint:gosec // G704: the fixture request built above
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return "", auth.ErrNotFound
	}
	if response.StatusCode != 200 {
		return "", errors.New("fixture store denied")
	}
	raw, err := io.ReadAll(response.Body)
	return string(raw), err
}
func (s desktopHTTPStore) Get(ctx context.Context, key string) (string, error) {
	return s.call(ctx, http.MethodGet, key, "")
}
func (s desktopHTTPStore) Set(ctx context.Context, key, value string) error {
	_, err := s.call(ctx, http.MethodPut, key, value)
	return err
}
func (s desktopHTTPStore) Delete(ctx context.Context, key string) error {
	_, err := s.call(ctx, http.MethodDelete, key, "")
	return err
}

// Only the fixture authorization-server hostname is redirected. Every management
// request still traverses the real HTTP transport to the separate Agent process.
type desktopFixtureTransport struct {
	base     http.RoundTripper
	endpoint string
}

func (t desktopFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() != "fixture.invalid" {
		return t.base.RoundTrip(request)
	}
	target, err := url.Parse(t.endpoint)
	if err != nil {
		return nil, err
	}
	clone := request.Clone(request.Context())
	u := *request.URL
	clone.URL = &u
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	return t.base.RoundTrip(clone)
}
