// SPDX-License-Identifier: Apache-2.0

// tunnel.go — `knowledge tunnel <env-name>`: open an SSH connection to a dev
// environment via the Fulminate relay. It ports the generic ephemeral-keypair →
// POST /v1/dev-vm/connect → short-lived-cert → SSH-over-relay flow that used to
// live in the agent repo's `fulminate dev connect`, RE-IMPLEMENTED here so the
// OSS knowledge client depends on NO agent/private module — only stdlib,
// golang.org/x/crypto/ssh, and this binary's own auth package.
//
// The bearer is the client's EXISTING shared cloud token (the same WorkOS token
// that authorizes cloud MCP/sync), so there is no separate login: a present
// KNOWLEDGE_AUTH_TOKEN machine bearer is honored FIRST (headless/executor path),
// otherwise the interactive keychain OAuth source is used — mirroring
// cli.BuildSyncTransport / bootstrap.selectAuthSources.
//
// The relay is served at the SAME HOST as the API, so the SSH host is DERIVED
// from the build-tag-pinned CloudEndpoint constant — no separate host constant,
// no --ssh-host flag, no env override.

package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// connectRequest / connectResponse are the client side of Contract A (the
// cert-issuance API served at POST /v1/dev-vm/connect). The field names MUST
// match the server's devssh.ConnectRequest/ConnectResponse. The knowledge
// client lives in a different module from the agent server and CANNOT import
// its devssh package, so it mirrors the field names by convention — the drift
// guard on each side (the server's contract_test.go and this file's
// tunnel_test.go) keeps them in lockstep.
type connectRequest struct {
	SSHPublicKey string `json:"ssh_public_key"`
	Env          string `json:"env,omitempty"`
}

type connectResponse struct {
	Certificate string `json:"certificate"`
	// RelayToken is the agent-minted, dev_env-scoped EdDSA relay_token the relay
	// verifies on the ws connect path. It mirrors the server's
	// devssh.ConnectResponse.RelayToken field by convention (same cross-module
	// no-import mirror as the rest of this contract). The --proxy path sends it in
	// the ws header; the direct-cert SSH path ignores it.
	RelayToken string `json:"relay_token"`
	// HostCAPubKey is the server's per-deploy SSH host-CA trust anchor (an OpenSSH
	// authorized-key line) the CLI installs as a per-invocation `@cert-authority`
	// known_hosts entry keyed on env_id, so ssh cryptographically verifies it reached
	// the right VM's sshd. It mirrors the server's
	// devssh.ConnectResponse.HostCAPubKey field by convention (tunnel_test.go guards
	// the drift). An EMPTY value means the server predates host verification: the
	// tunnel FAILS CLOSED (never connects unverified) rather than degrading.
	HostCAPubKey string `json:"host_ca_pubkey"`
}

// connectHTTPClient bounds the single cert-issuance round-trip.
var connectHTTPClient = &http.Client{Timeout: 30 * time.Second}

// sshLoginUser is the fixed Linux login user every dev-VM SSH session authenticates
// as. It MUST equal the sole principal on the minted user certificate (the agent
// server's devssh.SSHLoginUser) and the login user the dev-VM cloud-config creates.
// The connect endpoint no longer encodes tenancy in the cert principal — the
// relay_token routing + env-scoped host cert already pin this to exactly one env's
// VM — so ssh must present this one real username or sshd rejects the session with
// "name is not a listed principal". This lives in a different module/repo from the
// server and cannot share a package (no-shared-packages rule), so the literal is
// duplicated and guarded by tunnel_test.go on this side.
const sshLoginUser = "fulminate"

// tunnelUsage is printed by `knowledge tunnel --help`. Terse, factual.
const tunnelUsage = `knowledge tunnel — open an SSH connection to your dev environment

Usage:
  knowledge tunnel [--print-proxy-command] [--new | --reuse <name>] <env-name>
  knowledge tunnel --list-sessions <env-name>
  knowledge tunnel --kill <name> <env-name>
  knowledge tunnel --rename [<old>=]<new> <env-name>
  knowledge tunnel --command "<cmd>" <env-name>
  knowledge tunnel <env-name> -- <cmd...>

Fetches a short-lived SSH certificate for the named dev environment and opens
an SSH connection to it via the Fulminate relay. <env-name> selects which of
your account's environments to connect to (the per-account env label); omit it
to connect to your oldest environment.

Flags may appear before or after the env name. Command mode: pass
--command "<cmd>" or a "-- <cmd...>" passthrough (everything after a standalone
--) to run a single command non-interactively (no PTY), stream its stdout/stderr,
and exit with its remote status — SSM-style. A one-shot command attaches to no
tmux session.

Sessions: each connection attaches a named tmux session inside the dev VM. With
no flag, a stable per-env default named for this machine ("cli-<host>") is used
(persisted under ~/.knowledge/ssh/<env>.session), so re-running reattaches the
same session and a headless harness survives a disconnect — and it reads legibly
in --list-sessions, distinct from the web terminal's "web-<os>-<browser>" session.
--new rotates the default to a fresh human-readable "cli-<host>-<word>";
--reuse <name> (alias --session <name>) attaches a session by a label you choose,
creating it on demand, without changing the default; --list-sessions prints the
env's current sessions and exits; --kill <name> destroys a session and exits;
--rename <new> renames the default (--rename <old>=<new> renames a specific
session, and the persisted default follows the rename) and exits.

The ephemeral private key never leaves this machine — only the public key is
sent to be certified. The connection is tunneled through the Fulminate relay
via an SSH ProxyCommand (this binary in --proxy mode). Use
--print-proxy-command to emit the ~/.ssh/config ProxyCommand + identity lines
instead of connecting directly.
`

// TunnelCmd implements `knowledge tunnel <env-name>`. Returns nil on success; a
// non-nil error is printed to stderr + exit 1 by the caller (bootstrap
// RunSubcommand). Mirrors LoginCmd's flag/usage/signal shape — flag, not cobra.
//
// Flags may appear in ANY order relative to the env name (parseTunnelFlags
// permutes around it), and a standalone `--` separates the flag side from a
// verbatim remote command — so `tunnel <env> --new` and `tunnel <env> -- ls -la`
// both behave as written rather than leaking the trailing tokens into ssh.
func TunnelCmd(args []string) error {
	fs, tf := newTunnelFlagSet()

	// Split off the remote command (everything after a standalone `--`) BEFORE
	// flag parsing, then permute-parse the flag side so local flags are recognized
	// wherever they sit relative to the env positional.
	flagArgs, dashCmd := splitAtDoubleDash(args)

	env, err := parseTunnelFlags(fs, flagArgs)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// A one-shot remote command may be supplied EITHER via --command (a single
	// shell string) OR as a `-- <cmd...>` passthrough — never both.
	cmdTokens, err := resolveTunnelCommand(*tf.command, dashCmd)
	if err != nil {
		return err
	}

	// --reuse and --session are aliases for pinning a specific session name.
	pinned := *tf.session
	if pinned == "" {
		pinned = *tf.reuse
	}
	if *tf.session != "" && *tf.reuse != "" && *tf.session != *tf.reuse {
		return fmt.Errorf("--session and --reuse both set to different names (%q vs %q) — use one", *tf.session, *tf.reuse)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// --proxy is the transport leg invoked by ssh itself (via the emitted
	// ProxyCommand line); it dials the relay ws and pipes bytes, never spawning ssh.
	if *tf.proxy {
		return runProxy(ctx, CloudEndpoint, env)
	}

	return runTunnel(ctx, CloudEndpoint, env, tunnelOpts{
		printProxy:    *tf.printProxy,
		newSession:    *tf.newSession,
		pinnedSession: pinned,
		listSessions:  *tf.listSessions,
		killSession:   *tf.kill,
		renameSession: *tf.rename,
		command:       cmdTokens,
	})
}

// runTunnel wires the one-shot connect: token → keygen → cert fetch → on-disk
// identity → session-name resolution → ssh (or ProxyCommand emit). The SSH host is
// DERIVED from the API endpoint (the relay is served at the same host as the API).
func runTunnel(ctx context.Context, apiURL, env string, opts tunnelOpts) error {
	token, err := tunnelToken(ctx)
	if err != nil {
		return err
	}

	kp, err := generateEphemeralKey()
	if err != nil {
		return err
	}

	// The direct-cert SSH path needs the certificate (its identity), the host_ca_pubkey
	// (the trust anchor it installs to VERIFY the server), and the relay_token — only to
	// read its env_id claim, which is the host-cert principal ssh matches against.
	cert, relayToken, hostCAPubKey, err := fetchCertProving(ctx, connectHTTPClient, apiURL, token, kp.authorizedKey, env)
	if err != nil {
		return err
	}

	// FAIL CLOSED: host verification is NOT optional. An empty host_ca_pubkey
	// means the connect server predates host certificates, so this VM's SSH host identity
	// cannot be cryptographically verified — refuse to open an UNVERIFIED session rather
	// than silently degrading to no host-key checking (never-fail-open on a security
	// feature; pre-launch, no back-compat).
	if hostCAPubKey == "" {
		return fmt.Errorf("dev-VM connect server is too old: it returned no host_ca_pubkey, so this VM's SSH host identity cannot be verified — upgrade the deployment before connecting (host verification unavailable)")
	}

	// The env_id is the host-cert principal the server signed; ssh matches the presented
	// host certificate against the @cert-authority anchor keyed on it. It is read from the
	// relay_token's own claim (no trust decision — the same non-authoritative echo runProxy
	// uses; the cryptographic gate is the host cert's CA signature + principal match).
	envID, err := devEnvIDFromToken(relayToken)
	if err != nil {
		return err
	}

	dir, err := sshSessionDir()
	if err != nil {
		return err
	}
	keyPath, certPath, err := writeIdentityFiles(dir, connectionName(env), kp, cert)
	if err != nil {
		return err
	}
	knownHostsPath, err := writeKnownHosts(dir, connectionName(env), envID, hostCAPubKey)
	if err != nil {
		return err
	}

	// The SSH transport routes THROUGH the relay ws: ssh runs this binary in --proxy mode
	// as its ProxyCommand (which dials the relay and pipes bytes), rather than connecting
	// to the API host directly (it has no sshd). The ssh TARGET is fulminate@<env_id>: the
	// login user is the fixed sshLoginUser (the sole cert principal — sshd rejects any other
	// name), and env_id is the host-key-lookup name, so ssh matches the VM's presented host
	// certificate against the `@cert-authority <env_id>` line in the known_hosts we just wrote;
	// the transport is the ProxyCommand.
	proxyCmd := proxyCommandArg(sshSelf(), env)

	// --list-sessions: run a one-shot `tmux ls` through the NON-interactive
	// ForceCommand branch (ssh sets SSH_ORIGINAL_COMMAND), which enumerates the dev
	// env's existing tmux sessions to stdout for use with --reuse. It is itself a
	// one-shot non-interactive command (see runTunnelCommand) and deliberately does
	// NOT thread FULMINATE_SESSION — it neither attaches nor creates a session.
	// The non-interactive short-circuits (--list-sessions, --kill, one-shot command
	// mode) each run a single command over the ForceCommand's NON-interactive branch,
	// attach to no tmux session, and thread no FULMINATE_SESSION. runOneShot handles
	// whichever was requested; handled=true means we return its result rather than
	// falling through to the interactive session path.
	if handled, err := runOneShot(ctx, dir, env, keyPath, certPath, sshLoginUser+"@"+envID, knownHostsPath, proxyCmd, opts); handled {
		return err
	}

	// Resolve the tmux session name delivered to the VM via SetEnv/AcceptEnv
	// FULMINATE_SESSION: the persisted per-env default (reattach on re-run), a fresh
	// name (--new), or a pinned existing name (--reuse/--session). A generated or
	// rotated default is written back to the per-env sidecar; a pinned name is not.
	sessionName, persist, err := resolveSessionName(dir, env, opts.pinnedSession, opts.newSession)
	if err != nil {
		return err
	}
	if persist {
		if err := writeSessionSidecar(dir, env, sessionName); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "dev session: %s (use --new for a fresh one, --list-sessions to see all)\n", sessionName)

	if opts.printProxy {
		// Emit ~/.ssh/config Host-block directives: the ProxyCommand invokes this binary
		// in --proxy mode, the ephemeral identity + cert to present, the session name
		// (SetEnv FULMINATE_SESSION), AND the host-verification anchor (a per-invocation
		// known_hosts keyed on env_id, with StrictHostKeyChecking on + HostKeyAlias set
		// to env_id) so the pasted config cryptographically verifies the VM's sshd identity.
		fmt.Printf("ProxyCommand %s\n", proxyCmd)
		fmt.Printf("User %s\n", sshLoginUser)
		fmt.Printf("IdentityFile %s\n", keyPath)
		fmt.Printf("CertificateFile %s\n", certPath)
		fmt.Println("IdentitiesOnly yes")
		fmt.Printf("SetEnv FULMINATE_SESSION=%s\n", sessionName)
		fmt.Printf("UserKnownHostsFile %s\n", knownHostsPath)
		fmt.Println("StrictHostKeyChecking yes")
		fmt.Printf("HostKeyAlias %s\n", envID)
		fmt.Fprintln(os.Stderr, "Paste the lines above into your ~/.ssh/config Host block.")
		return nil
	}
	args := buildSSHArgs(keyPath, certPath, sshLoginUser+"@"+envID, []string{
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ProxyCommand=" + proxyCmd,
		"-o", "SetEnv=FULMINATE_SESSION=" + sessionName,
	})
	return sshRunner(ctx, args)
}

// tunnelToken obtains the WorkOS cloud access token that authorizes the connect
// request. It REUSES the client's existing shared cloud token source: a present
// KNOWLEDGE_AUTH_TOKEN machine bearer is honored FIRST (the headless/executor
// path has no keychain, so the OAuth-refresh branch would fail there), otherwise
// the interactive keychain OAuth source is used. This is the exact
// machine-token-first shape at cli.BuildSyncTransport (sync_transport.go:36-44)
// and bootstrap.selectAuthSources — the same token that authorizes cloud
// MCP/sync is accepted by /v1/dev-vm/connect, so there is no separate login.
func tunnelToken(ctx context.Context) (string, error) {
	var ts auth.TokenSource
	if tok := os.Getenv("KNOWLEDGE_AUTH_TOKEN"); tok != "" {
		ts = auth.StaticTokenSource{AccessToken: tok}
	} else {
		store, err := openStore()
		if err != nil {
			return "", fmt.Errorf("tunnel requires login — keychain unavailable: %w", err)
		}
		ts = auth.NewOAuthTokenSource(store, CloudEndpoint, AllowedAuthHosts())
	}
	token, _, err := ts.Token(ctx)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("no cloud token available — run `knowledge login`")
	}
	return token, nil
}

// tunnelSSHHost derives the relay SSH host from the API endpoint by parsing it
// and taking the host component (e.g. "https://fulminate.io" → "fulminate.io",
// "https://dev.fulminate.io" → "dev.fulminate.io"). The relay is served at the
// SAME HOST as the API, so the build-tag-pinned CloudEndpoint is the only
// prod/dev switch and the tunnel host inherits it for free.
func tunnelSSHHost(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint %q: %w", endpoint, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("endpoint %q has no host component", endpoint)
	}
	return u.Host, nil
}

// ephemeralKeyPair is a freshly generated ed25519 keypair. The private key is
// held only in-process + a 0600 file; only authorizedKey is ever transmitted.
type ephemeralKeyPair struct {
	privatePEM    []byte
	authorizedKey string
}

// generateEphemeralKey creates an ed25519 keypair in OpenSSH formats.
func generateEphemeralKey() (*ephemeralKeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "fulminate-dev")
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return &ephemeralKeyPair{
		privatePEM:    pem.EncodeToMemory(block),
		authorizedKey: string(ssh.MarshalAuthorizedKey(sshPub)),
	}, nil
}

// fetchCert POSTs the ephemeral PUBLIC key (never the private key) to Contract A
// with the bearer token and returns the marshaled certificate, the relay_token (the
// dev_env-scoped credential the ws --proxy path presents to the relay), AND the
// host_ca_pubkey (the host-CA trust anchor the direct-cert path installs as an
// @cert-authority known_hosts entry to verify the server's identity). Distinct HTTP
// statuses map to distinct, actionable errors.
func fetchCert(ctx context.Context, client *http.Client, apiURL, token, publicKey, env string) (cert, relayToken, hostCAPubKey string, err error) {
	body, err := json.Marshal(connectRequest{SSHPublicKey: publicKey, Env: env})
	if err != nil {
		return "", "", "", fmt.Errorf("encode request: %w", err)
	}
	reqURL := strings.TrimRight(apiURL, "/") + "/v1/dev-vm/connect"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return "", "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	// This path sets its bearer BY HAND rather than riding the sync transport's
	// stamping chokepoint, which is exactly why it went unstamped: nothing here
	// passes through the one place that stamps everything else. Use the shared
	// implementation rather than a literal header pair, so this site cannot
	// drift from it silently — the census would still list the site, so a drift
	// here is precisely what no gate would notice.
	clientver.Stamp(req.Header)

	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("connect request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var out connectResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", "", "", fmt.Errorf("decode certificate response: %w", err)
		}
		if out.Certificate == "" {
			return "", "", "", fmt.Errorf("connect returned an empty certificate")
		}
		return out.Certificate, out.RelayToken, out.HostCAPubKey, nil
	case http.StatusBadRequest:
		return "", "", "", fmt.Errorf("connect rejected the SSH public key (400)")
	case http.StatusUnauthorized:
		return "", "", "", fmt.Errorf("your session has expired — run `knowledge login` again (401)")
	case http.StatusForbidden:
		return "", "", "", fmt.Errorf("not authorized: no matching dev environment is available to your account (403)")
	case http.StatusServiceUnavailable:
		return "", "", "", fmt.Errorf("dev-VM SSH is unavailable on this deployment (503)")
	case http.StatusUpgradeRequired:
		// A version refusal. Routed through the shared classifier so a refused
		// tunnel names the minimum, this client's version and the upgrade
		// command, rather than falling to the default arm and reporting a bare
		// status the user cannot act on.
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, auth.MaxErrorBodyBytes))
		if refusal, ok := auth.LatchVersionRefusal(auth.RefusalObservation{
			Status:    resp.StatusCode,
			Header:    resp.Header,
			Body:      raw,
			ReadErr:   readErr,
			Transport: "tunnel",
			Path:      reqURL,
		}); ok {
			return "", "", "", refusal
		}
		return "", "", "", fmt.Errorf("connect refused this client's version (426): %s", strings.TrimSpace(string(raw)))
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", "", fmt.Errorf("connect failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
}
