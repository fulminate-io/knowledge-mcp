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
	var configPath string
	for i, arg := range args {
		if arg == "--config-file" && i+1 < len(args) {
			configPath = args[i+1]
		}
	}
	buildSyncTransportFn = func() (*auth.Transport, error) {
		return auth.NewSyncTransport(endpoint, auth.NewReadOnlyTokenSource(store), auth.WithAccountSelection(auth.NewAccountSelection(configPath, time.Second))), nil
	}
	if args[0] == "desktop-remote" {
		if relay := os.Getenv("DESKTOP_DASHBOARD_RELAY"); relay != "" {
			dashboardRelayURL = func() (string, error) { return relay, nil }
		}
		base := http.DefaultTransport
		http.DefaultTransport = desktopFixtureTransport{base: base, endpoint: endpoint}
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

type desktopHTTPStore struct{ endpoint string }

func (s desktopHTTPStore) call(ctx context.Context, method, key, value string) (string, error) {
	body, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, method, s.endpoint+"/store/"+url.PathEscape(key), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
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
