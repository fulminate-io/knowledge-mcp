# Run with Docker

`ghcr.io/fulminate-io/knowledge-mcp` packages the knowledge MCP server as a
single image: the client, its graph server, and a small front process that owns
the container. It serves the same MCP endpoint the local install does, so an
editor cannot tell the difference.

The image is built `FROM scratch` — no shell, no package manager, no users. It
exposes one port, 15023, and keeps everything it remembers under one volume,
`/data`.

## The three ways to run it

### 1. A local stack over HTTP

```sh
docker run -d --name knowledge \
  -p 127.0.0.1:15023:15023 \
  -v knowledge-data:/data \
  -v "$PWD:$PWD:ro" \
  ghcr.io/fulminate-io/knowledge-mcp:latest
```

Then point your editor at `http://127.0.0.1:15023/mcp`.

The `-v "$PWD:$PWD:ro"` line is what lets the server index your code:
`collect` can only reach repositories mounted into the container, so without it
the server can answer questions on graphs it already holds but cannot build new
ones. Mount the repository at the **same absolute path it has on the host** —
that keeps one path space on both sides, so the path your editor's agent already
uses is the `collect` id, the graph is named after the real repository directory,
and every file reference the server returns resolves on the host. Mount more
repositories the same way (`-v "$HOME/code/other:$HOME/code/other:ro"`). Mounts
are fixed at container creation — adding a repository later means recreating the
container with the extra `-v`, which loses nothing because all state lives on
the `knowledge-data` volume. On Windows, where a host path cannot be reproduced
inside a Linux container, mount at a stable path such as `/workspace` and use
that container path as the `collect` id instead.

**Publish to `127.0.0.1` and not to `0.0.0.0`.** The MCP endpoint is
unauthenticated, and that loopback binding on the host is what stops anyone else
reaching it — it is the access control, not a formatting preference. Dropping the
`127.0.0.1:` prefix publishes an unauthenticated endpoint to every machine that
can route to yours.

### 2. A stdio MCP client

Most editors spawn their MCP server as a subprocess and talk over stdin/stdout.
Use that mode by passing `--stdio` and running the container interactively:

```sh
docker run -i --rm -v knowledge-data:/data ghcr.io/fulminate-io/knowledge-mcp:latest --stdio
```

Configure that whole command as the client's spawn command. Nothing is published
in this mode — no `-p` flag, no listening socket on the host — so it has no
network exposure at all.

### 3. Collecting a repository in CI

Mount the checkout, give the container a machine token, and drive `collect` as an
MCP call:

```sh
docker run -i --rm \
  -e KNOWLEDGE_AUTH_TOKEN \
  -v "$PWD:$PWD:ro" \
  -v knowledge-data:/data \
  ghcr.io/fulminate-io/knowledge-mcp:latest --stdio
```

`KNOWLEDGE_AUTH_TOKEN` routes the client **cloud-only with no local fallback**,
so a CI run with a wrong, expired, or under-scoped token fails its tool calls
rather than falling back to a local graph. See [Signing in](#signing-in).

`collect` is an MCP tool rather than a subcommand, so it is driven as a
`tools/call` like any other tool. The `id` must be an absolute path as the
container sees it — with the same-path mount above, that is simply the
checkout's own absolute path, and the graph takes the repository's real name:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"collect","arguments":{"type":"code","id":"/home/runner/work/app/app"}}}
```

The same call works over the published HTTP endpoint if you are running the
container from use case 1 instead.

## Signing in

### Interactive login

Run `knowledge login` in a second container that joins the serving container's
network:

```sh
docker run --rm -it --network container:knowledge -v knowledge-data:/data --entrypoint /app/knowledge ghcr.io/fulminate-io/knowledge-mcp:latest login
```

It prints a sign-in URL. Open it in your own browser — the container has no
browser of its own, which is why the URL is printed rather than launched.
Finish signing in and the tab lands on a "knowledge login complete" page served
from inside the container.

Three parts of that command are doing real work:

- **`--network container:knowledge`** puts the login container in the serving
  container's network namespace, so its callback listener and the front process
  share a loopback interface and the front process can forward your browser's
  callback inward — on the port you have *already* published. Nothing new is
  published and nothing new listens beyond loopback. The name `knowledge` is the
  `--name knowledge` from the run command in use case 1 above; if you named your
  container something else, use that name here.
- **`-v knowledge-data:/data`** must be the same named volume the serving
  container uses, and leaving it out fails in the worst possible way. Joining a
  network namespace shares no mounts, and the image declares `/data` as a
  volume, so a login container started without `-v` gets a fresh anonymous
  volume — which `--rm` deletes on exit. The sign-in succeeds, the credential is
  written, the container prints "Logged in." and exits 0, the volume is
  destroyed, and you are still logged out with nothing anywhere saying so.
- **`--entrypoint /app/knowledge`** runs the client directly instead of the
  front process, so the container runs one command and exits.

**Publishing on a nonstandard port.** The redirect URL has to name the port your
browser dials, which is the host side of your publish. If you published
`-p 127.0.0.1:18080:15023`, pass `-e KNOWLEDGE_LOGIN_REDIRECT_PORT=18080` to the
login container. The default is 15023.

**No restart needed.** A serving container that is already running picks the new
credential up within about five seconds — it re-reads the credential store once
its cached answer expires. Restarting it changes nothing.

**Where the credential lands.** The container has no OS keychain, so the
credential is written to `~/.knowledge/credentials` on the `/data` volume with
mode 0600. On a host with a working keychain, the keychain is still used and no
such file is written.

**Known limitation.** The file store engages when the OS credential backend is
*unreachable*. An environment whose dbus session bus is present but broken —
as opposed to absent — can still fail the login rather than falling back, because
the classifier that decides this is deliberately narrow and prefers surfacing an
error to silently writing a credential in plaintext. This is suspected rather
than reproduced; it is written down so that anyone who hits it recognises it.

### Machine token

Set `KNOWLEDGE_AUTH_TOKEN` in the container environment. The client reads it
automatically; nothing else needs configuring. A machine token routes
**cloud-only with no local fallback**: it short-circuits routing before the
local graph is ever considered, and a client holding one never starts a local
server. A token that is wrong, expired, or missing the scope you need therefore
fails every tool call outright rather than quietly degrading to the local graph.

### Local only

Run without a token and without logging in. Everything works against the local
graph; only cloud-backed features are unavailable.

## Volumes, ports, and file ownership

`/data` holds the graph, and the container's home directory resolves there, so
everything the server remembers lives on that one volume. Give it a **named
volume** and restarts keep their memory; omit it and each run starts empty.

Port 15023 is the only port the image exposes.

The container runs as uid `65532`. A named volume inherits the right ownership
from the image and needs nothing from you. A **bind mount** of a host directory
arrives with its host ownership instead, so if you bind-mount `/data` rather than
using a named volume, either pre-`chown` the directory or run as yourself:

```sh
docker run --user "$(id -u):$(id -g)" -v "$PWD/knowledge-data:/data" ...
```

Mounting a repository read-only needs none of this.

## What the image does not have

There is no option — flag or environment variable — to change what the processes
inside the container listen on. The internal addresses are fixed at build time.
Control exposure with Docker's own publishing, which is what the `127.0.0.1:`
prefix in use case 1 is doing.
