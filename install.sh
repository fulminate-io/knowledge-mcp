#!/bin/sh
# install.sh — one-line installer for knowledge.
#
#   curl -fsSL https://raw.githubusercontent.com/fulminate-io/knowledge-mcp/main/install.sh | sh
#
# Thin POSIX-sh bootstrap (no bashisms): detect os/arch, download the
# knowledge client + server release tarballs from the PUBLIC
# github.com/fulminate-io/knowledge-mcp releases, sha256-verify BOTH,
# install them to ~/.knowledge/bin (no sudo, no /usr/local), then hand
# off to `knowledge setup` for config + assets + service units.
#
# Flags forwarded to `knowledge setup`: --headless --reconfigure
# --no-claude --no-codex --no-service --no-mcp. Script-only flags
# (consumed here, never forwarded): --version <tag> (pin the release),
# --force-script (install alongside a Homebrew-managed knowledge). No
# API-key/token/secret flags — credentials come from the environment or
# ~/.knowledge/config, never argv.

set -eu

REPO="fulminate-io/knowledge-mcp"
GITHUB="https://github.com"
GITHUB_API="https://api.github.com"
INSTALL_DIR="${HOME}/.knowledge/bin"

HEADLESS=0
FORCE_SCRIPT=0
PIN_VERSION=""
FORWARD="" # curated setup-understood flags, space-separated

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

# --- argument parse: PARSE OWN ARGS, never splat "$@" onto setup ---
while [ $# -gt 0 ]; do
	case "$1" in
	--headless)
		HEADLESS=1
		FORWARD="${FORWARD} --headless"
		;;
	--reconfigure) FORWARD="${FORWARD} --reconfigure" ;;
	--no-claude) FORWARD="${FORWARD} --no-claude" ;;
	--no-codex) FORWARD="${FORWARD} --no-codex" ;;
	--no-service) FORWARD="${FORWARD} --no-service" ;;
	--no-mcp) FORWARD="${FORWARD} --no-mcp" ;;
	--force-script) FORCE_SCRIPT=1 ;;
	--version)
		shift
		PIN_VERSION="${1:-}"
		[ -n "$PIN_VERSION" ] || fail "--version requires a tag argument"
		;;
	--version=*) PIN_VERSION="${1#--version=}" ;;
	*) fail "unknown flag: $1" ;;
	esac
	shift
done

# is_interactive: interactive only when --headless was NOT passed AND
# stdin is a TTY. The canonical `curl … | sh` one-liner pipes stdin
# (non-TTY) so it is non-interactive even without --headless.
# KNOWLEDGE_INSTALL_ASSUME_TTY is a test-only seam (mirrors setup's
# injected TTY classification) so the interactive branches stay drivable
# in non-TTY CI.
is_interactive() {
	[ "$HEADLESS" -eq 0 ] || return 1
	if [ -n "${KNOWLEDGE_INSTALL_ASSUME_TTY:-}" ]; then
		return 0
	fi
	[ -t 0 ]
}

# --- (1) detect os/arch ---
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
Linux) os="linux" ;;
Darwin) os="darwin" ;;
*) fail "unsupported OS '$os' — native Windows is not supported; use WSL (https://learn.microsoft.com/windows/wsl/) or download manually." ;;
esac
case "$arch" in
x86_64 | amd64) arch="amd64" ;;
arm64 | aarch64) arch="arm64" ;;
*) fail "unsupported architecture '$arch'." ;;
esac
if [ "$os" = "darwin" ] && [ "$arch" = "amd64" ]; then
	fail "darwin-amd64 (Intel Mac) is not a supported release target — build from source or use an arm64 Mac."
fi
PLATFORM="${os}-${arch}"

# --- (2) brew coexistence guard ---
if command -v brew >/dev/null 2>&1 && brew list knowledge >/dev/null 2>&1; then
	if is_interactive; then
		echo "install.sh: knowledge is already installed via Homebrew." >&2
		echo "  Upgrade the brew install with: brew upgrade knowledge" >&2
		echo "  Or re-run with --force-script to install the standalone script version alongside it." >&2
		if [ "$FORCE_SCRIPT" -ne 1 ]; then
			printf "install.sh: install the standalone script version anyway? [y/N]: "
			read -r brew_ans
			case "$brew_ans" in
			y | Y | yes | YES) : ;;
			*)
				echo "install.sh: keeping the Homebrew install; nothing changed."
				exit 0
				;;
			esac
		fi
	elif [ "$FORCE_SCRIPT" -ne 1 ]; then
		fail "knowledge is managed by Homebrew — run \`brew upgrade knowledge\`, or re-run with --force-script to override."
	fi
fi

# --- (3) resolve the release tag ---
if [ -n "$PIN_VERSION" ]; then
	TAG="$PIN_VERSION"
else
	TAG="$(curl -fsSL "$GITHUB_API/repos/$REPO/releases/latest" |
		grep '"tag_name"' | head -1 |
		sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/')"
	[ -n "$TAG" ] || fail "could not resolve the latest release tag from the GitHub API."
fi

# --- (4) version short-circuit: skip ONLY when the installed client
# version equals the target tag AND the server binary is present. A
# current client next to a MISSING server must still re-download. ---
if [ -x "$INSTALL_DIR/knowledge" ] && [ -x "$INSTALL_DIR/knowledge-server" ]; then
	installed_ver="$("$INSTALL_DIR/knowledge" --version 2>/dev/null || true)"
	case "$installed_ver" in
	*"$TAG"*)
		echo "install.sh: already up to date ($TAG); nothing to download."
		# shellcheck disable=SC2086 # FORWARD is a curated flag list; intentional word-split
		exec "$INSTALL_DIR/knowledge" setup --no-self-update ${FORWARD}
		;;
	esac
fi

# --- (5) download BOTH tarballs + checksums.txt ---
DL="$GITHUB/$REPO/releases/download/$TAG"
CLIENT_ASSET="knowledge-${PLATFORM}.tar.gz"
SERVER_ASSET="knowledge-server-${PLATFORM}.tar.gz"

STAGING="${HOME}/.knowledge/.staging.$$"
cleanup() { rm -rf "$STAGING"; }
trap cleanup EXIT INT TERM
rm -rf "$STAGING"
mkdir -p "$STAGING"

echo "install.sh: downloading knowledge $TAG for $PLATFORM"
curl -fsSL -o "$STAGING/$CLIENT_ASSET" "$DL/$CLIENT_ASSET" || fail "download failed: $CLIENT_ASSET"
curl -fsSL -o "$STAGING/$SERVER_ASSET" "$DL/$SERVER_ASSET" || fail "download failed: $SERVER_ASSET"
curl -fsSL -o "$STAGING/checksums.txt" "$DL/checksums.txt" || fail "download failed: checksums.txt"

# --- (6) sha256-verify BOTH tarballs; abort (installing nothing) on any
# mismatch or missing checksums entry ---
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}
verify_asset() {
	# $1 = staged file, $2 = asset name
	want="$(awk -v a="$2" '{n=$2; sub(/^\*/,"",n)} n==a {print $1; exit}' "$STAGING/checksums.txt")"
	[ -n "$want" ] || fail "checksums.txt has no entry for $2 — aborting, nothing installed."
	got="$(sha256_of "$1")"
	[ "$got" = "$want" ] || fail "sha256 mismatch for $2 (expected $want, got $got) — aborting, nothing installed."
}
verify_asset "$STAGING/$CLIENT_ASSET" "$CLIENT_ASSET"
verify_asset "$STAGING/$SERVER_ASSET" "$SERVER_ASSET"

# --- (7) two-phase install: extract BOTH into staging first; only after
# BOTH extract cleanly, move the two binaries into ~/.knowledge/bin LAST
# via adjacent atomic renames (same filesystem). Any earlier failure
# leaves a pre-existing ~/.knowledge/bin byte-untouched (no upgrade skew;
# a fresh install stays empty). ---
tar -xzf "$STAGING/$CLIENT_ASSET" -C "$STAGING" || fail "extract failed: $CLIENT_ASSET"
tar -xzf "$STAGING/$SERVER_ASSET" -C "$STAGING" || fail "extract failed: $SERVER_ASSET"
[ -f "$STAGING/knowledge" ] || fail "archive $CLIENT_ASSET did not contain a 'knowledge' binary."
[ -f "$STAGING/knowledge-server" ] || fail "archive $SERVER_ASSET did not contain a 'knowledge-server' binary."

mkdir -p "$INSTALL_DIR"
chmod 0755 "$STAGING/knowledge" "$STAGING/knowledge-server"
mv -f "$STAGING/knowledge" "$INSTALL_DIR/knowledge"
mv -f "$STAGING/knowledge-server" "$INSTALL_DIR/knowledge-server"
echo "install.sh: installed knowledge + knowledge-server to $INSTALL_DIR"

# --- (8) PATH hint; append to a shell rc ONLY on explicit interactive
# consent (never in non-interactive / piped mode) ---
case ":${PATH}:" in
*":$INSTALL_DIR:"*) : ;; # already on PATH
*)
	echo "install.sh: add knowledge to your PATH:"
	echo "  export PATH=\"\$HOME/.knowledge/bin:\$PATH\""
	if is_interactive; then
		printf "install.sh: append that line to your shell rc now? [y/N]: "
		read -r rc_ans
		case "$rc_ans" in
		y | Y | yes | YES)
			case "${SHELL:-}" in
			*zsh) RC="${HOME}/.zshrc" ;;
			*) RC="${HOME}/.profile" ;;
			esac
			# shellcheck disable=SC2016 # literal $HOME/$PATH belongs in the rc file verbatim
			printf '\nexport PATH="$HOME/.knowledge/bin:$PATH"\n' >>"$RC"
			echo "install.sh: appended to $RC"
			;;
		esac
	fi
	;;
esac

# --- (9) handoff: exec the just-installed client with a CURATED forward
# — ALWAYS --no-self-update (install.sh already installed the current
# release; re-downloading would be redundant) plus only the setup-
# understood behavior flags actually received. Script-only flags
# (--force-script, --version) are STRIPPED. ---
# shellcheck disable=SC2086 # FORWARD is a curated flag list; intentional word-split
exec "$INSTALL_DIR/knowledge" setup --no-self-update ${FORWARD}
