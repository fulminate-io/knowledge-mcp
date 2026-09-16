// SPDX-License-Identifier: Apache-2.0

package cli

// THE MANIFEST — the checked-in, independent expectation the census gate
// compares the tree against.
//
// It is checked in rather than derived because a test that only COUNTED sites
// would pass at any count, and a test that derived its expectation from the same
// walk that produces the sites would supply its own answer key. Reviewing a diff
// of this file is how a future author is forced to state a disposition for a new
// outbound path instead of adding one silently.
//
// ADDING A ROW IS A DECISION, not bookkeeping. Under a version gate whose
// purpose is to be able to BLOCK clients with exploitable issues, one ungated
// path defeats the block — so "is this call Fulminate-bound, and if so what
// stamps it?" has to be answered here, in review, for every new site.
//
// Symbol is the callee text plus a 1-based occurrence ordinal within the file,
// deliberately NOT a line number: a line number rots on every edit above it and
// would turn this gate red on changes that touch no call path.
var fulminateCallPathManifest = []censusRow{
	// Dashboard relay setup reaches the existing stamped websocket handshake.
	{File: "internal/cli/desktop_dashboard_http.go", Symbol: "proxyWSURL#1", Disposition: dispReaches, Reaches: "internal/cli/tunnel_proxy.go:<recv>.DialContext#1"},
	// The SSH channel and HTTP request target the user's saved environment service,
	// not a Fulminate API. Never forward account credentials or identity headers.
	{File: "internal/cli/desktop_dashboard_http.go", Symbol: "client.DialContext#1", Disposition: dispExcluded},
	{File: "internal/cli/desktop_dashboard_http.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	// Native management uses the same stamped transport and AuthKit credential source.
	{File: "internal/cli/desktop_auth.go", Symbol: "auth.NewSyncTransport#1", Disposition: dispReaches, Reaches: "internal/auth/sync_transport.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/desktop_auth.go", Symbol: "auth.NewOAuthTokenSource#1", Disposition: dispExcluded},
	{File: "internal/cli/desktop_remote.go", Symbol: "auth.NewSyncTransport#1", Disposition: dispReaches, Reaches: "internal/auth/sync_transport.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/desktop_remote.go", Symbol: "auth.NewOAuthTokenSource#1", Disposition: dispExcluded},
	// The Desktop platform proxy rides the same stamped transport: it forwards
	// an allowlisted platform path with the bearer this process holds, so it is
	// Fulminate-bound and reaches the sync transport's single stamping point.
	{File: "internal/cli/desktop_platform.go", Symbol: "auth.NewSyncTransport#1", Disposition: dispReaches, Reaches: "internal/auth/sync_transport.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/desktop_platform.go", Symbol: "auth.NewOAuthTokenSource#1", Disposition: dispExcluded},
	// ---------------------------------------------------------------------
	// FULMINATE-BOUND. The population this gate exists to cover.
	// ---------------------------------------------------------------------

	// The single stamping point for the whole /v1/sync/* surface: graph push,
	// the control channel, and the version-challenge exchange all reach it.
	{File: "internal/auth/sync_transport.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispStampedHere},
	// The connect Engine/Ingest/Health path. It CLONES rather than constructs,
	// which is why the walk carries a request-clone shape at all — and stamping
	// on the clone is what puts the headers on the 401-refresh retry as well as
	// the first send.
	{File: "internal/graphclient/cloud_auth.go", Symbol: "req.Clone#1", Disposition: dispStampedHere},
	// The tunnel certificate POST to /v1/dev-vm/connect. It sets Authorization
	// by hand rather than riding the sync transport's chokepoint, which is
	// exactly why it went unstamped until this ticket.
	{File: "internal/cli/tunnel.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispStampedHere},
	// The relay websocket handshake to /v1/dev-vm/tunnel-ws on the same host.
	// Found only by the shape half of the census: it names no endpoint constant
	// in an argument, so an endpoint-keyed sweep alone could not see it.
	{File: "internal/cli/tunnel_proxy.go", Symbol: "<recv>.DialContext#1", Disposition: dispStampedHere},
	// ONE helper, TWO targets. The RFC 9728 protected-resource GET at the
	// Fulminate API base URL is ours and is stamped; the RFC 8414
	// authorization-server GET at the AuthKit host is a third party and is NOT.
	// The caller decides, so the classification lives in one place rather than
	// being re-derived from a URL comparison inside the helper.
	{File: "internal/auth/discovery.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispStampedHere},

	// ---------------------------------------------------------------------
	// REACHES-A-DISPOSITIONED-SITE. Constructors and entry points whose own
	// downstream request site carries the real disposition.
	// ---------------------------------------------------------------------

	{File: "internal/cli/sync_transport.go", Symbol: "auth.NewSyncTransport#1", Disposition: dispReaches,
		Reaches: "internal/auth/sync_transport.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/sync_transport.go", Symbol: "auth.NewSyncTransport#2", Disposition: dispReaches,
		Reaches: "internal/auth/sync_transport.go:http.NewRequestWithContext#1"},
	{File: "internal/bootstrap/client_construct.go", Symbol: "auth.NewSyncTransport#1", Disposition: dispReaches,
		Reaches: "internal/auth/sync_transport.go:http.NewRequestWithContext#1"},
	{File: "internal/bootstrap/client_construct.go", Symbol: "graphclient.NewRouterWithMachineAuth#1", Disposition: dispReaches,
		Reaches: "internal/graphclient/cloud_auth.go:req.Clone#1"},
	{File: "internal/cli/auth_login.go", Symbol: "discoverFn#1", Disposition: dispReaches,
		Reaches: "internal/auth/discovery.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/desktop_auth.go", Symbol: "discoverFn#1", Disposition: dispReaches,
		Reaches: "internal/auth/discovery.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/desktop_auth.go", Symbol: "discoverFn#2", Disposition: dispReaches,
		Reaches: "internal/auth/discovery.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/auth_logout.go", Symbol: "discoverFn#1", Disposition: dispReaches,
		Reaches: "internal/auth/discovery.go:http.NewRequestWithContext#1"},
	{File: "internal/cli/tunnel.go", Symbol: "runProxy#1", Disposition: dispReaches,
		Reaches: "internal/cli/tunnel_proxy.go:<recv>.DialContext#1"},
	{File: "internal/cli/tunnel.go", Symbol: "runTunnel#1", Disposition: dispReaches,
		Reaches: "internal/cli/tunnel.go:http.NewRequestWithContext#1"},

	// ---------------------------------------------------------------------
	// EXCLUDED-WITH-REASON. Not calls to Fulminate services. Every row is
	// REQUIRED: once the walk covers the whole client tree, an unlisted site is
	// a gate failure, which is the point.
	// ---------------------------------------------------------------------

	// Credential constructors. Their OWN outbound requests are the AuthKit legs
	// below, both of which are excluded — so labeling these
	// reaches-a-dispositioned-site would record a constructor as covered while
	// the request it produces is recorded as out of scope. The Fulminate-bound
	// transport each one feeds carries its own row.
	{File: "internal/cli/sync_transport.go", Symbol: "auth.NewOAuthTokenSource#1", Disposition: dispExcluded},
	{File: "internal/cli/tunnel.go", Symbol: "auth.NewOAuthTokenSource#1", Disposition: dispExcluded},
	{File: "internal/bootstrap/client_construct.go", Symbol: "auth.NewOAuthTokenSource#1", Disposition: dispExcluded},

	// The WorkOS-hosted AuthKit authorization server. A third party: sending it
	// a client-version header tells it something about our users for no benefit
	// we can name, and the host is a CNAME we do not control.
	{File: "internal/auth/oauth_common.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/auth/dcr.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},

	// GCS V4 presigned URLs — direct to storage, never through the gateway. The
	// package is separately guarded by a corpus check that fails if any
	// client-identity header reaches it in any spelling.
	{File: "internal/syncgcs/gcs_http.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/syncgcs/gcs_http.go", Symbol: "http.NewRequestWithContext#2", Disposition: dispExcluded},

	// Public release artifacts and metadata.
	{File: "internal/bootstrap/install_http.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/bootstrap/install_http.go", Symbol: "http.NewRequestWithContext#2", Disposition: dispExcluded},

	// Third-party APIs the collectors and providers talk to.
	{File: "internal/backends/linear/client.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/collector/web/fetch.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/collector/web/github_materializer_fetch.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/embed/cohere.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/embed/gemini.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/embed/openaicompat.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/embed/voyage.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/llm/anthropic/anthropic.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/llm/gemini/gemini.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/llm/openai/service.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/rerank/voyage.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	// The first-run guide's provider check. It reads the user's own
	// provider's model list with the user's own key — the same third-party
	// hosts the generate arms above call, reached through the same
	// exported base-URL constants. It never touches a Fulminate service,
	// and local use of the guide requires no Fulminate account at all, so
	// a client-version header here would tell a third party something
	// about our users for no benefit we can name.
	{File: "internal/llm/providercheck/providercheck.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	// The custom-collector http provider. It is a THIRD PARTY BY DEFINITION —
	// somebody else's MCP server, named by an operator's own config entry — so
	// a client-version header would tell an unrelated host something about our
	// users for no benefit. The clone is where the operator's OWN headers are
	// set, and the daemon composes none of its own there: two corpus checks
	// watch that a header value never comes out of the daemon's environment.
	{File: "internal/externalcollector/mcphost.go", Symbol: "req.Clone#1", Disposition: dispExcluded},

	// Loopback requests to this machine's own daemon, and the TCP dialers
	// beneath the transports above. No Fulminate host, and for the dialers no
	// header surface at all.
	{File: "internal/bootstrap/lifecycle_subcommand.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/bootstrap/version_subcommand.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/bootstrap/version_subcommand.go", Symbol: "d.DialContext#1", Disposition: dispExcluded},
	// `knowledge check run` asks the daemon on this machine to perform the
	// corpus scan, over the same loopback MCP endpoint the version probe above
	// uses and with the same h2c transport shape. The request never leaves the
	// host: the endpoint is built from a literal 127.0.0.1 and a port flag, and
	// whatever the daemon does NEXT is stamped where the daemon does it.
	{File: "internal/bootstrap/check_daemon_route.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/bootstrap/check_daemon_route.go", Symbol: "d.DialContext#1", Disposition: dispExcluded},
	{File: "internal/bootstrap/lifecycle.go", Symbol: "net.DialTimeout#1", Disposition: dispExcluded},
	// Service lifecycle probes use literal 127.0.0.1 with the requested local
	// port: TCP listener detection and MCP initialize for runtime identity.
	// Neither targets a Fulminate service.
	{File: "internal/bootstrap/service_command.go", Symbol: "net.DialTimeout#1", Disposition: dispExcluded},
	{File: "internal/bootstrap/service_runtime.go", Symbol: "http.NewRequestWithContext#1", Disposition: dispExcluded},
	{File: "internal/graphclient/client.go", Symbol: "d.DialContext#1", Disposition: dispExcluded},
	{File: "internal/graphclient/upload_meter.go", Symbol: "<recv>.DialContext#1", Disposition: dispExcluded},
}
