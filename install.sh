#!/bin/sh
# install.sh — one-line installer for knowledge.
#
#   curl -fsSL https://raw.githubusercontent.com/fulminate-io/knowledge-mcp/main/install.sh | sh
#
# Thin POSIX-sh bootstrap (no bashisms): detect os/arch, download the
# knowledge client + server release tarballs from the PUBLIC
# github.com/fulminate-io/knowledge-mcp releases, sha256-verify BOTH,
# install them to ~/.knowledge/bin (no sudo, no /usr/local), add that
# directory to PATH via a marked, re-run-safe block in the rc file of
# your default shell, then hand off to `knowledge setup` for config +
# assets + service units.
#
# Flags forwarded to `knowledge setup`: --headless --reconfigure
# --no-claude --no-codex --no-service --no-mcp. Script-only flags
# (consumed here, never forwarded): --version <tag> (pin the release),
# --force-script (install alongside a Homebrew-managed knowledge),
# --no-modify-path (leave your shell rc files alone). For the piped
# one-liner the environment variable is the practical opt-out:
#
#   curl -fsSL https://raw.githubusercontent.com/fulminate-io/knowledge-mcp/main/install.sh | KNOWLEDGE_INSTALL_NO_PATH=1 sh
#
# No API-key/token/secret flags — credentials come from the environment or
# ~/.knowledge/config, never argv.

set -eu

REPO="fulminate-io/knowledge-mcp"
GITHUB="https://github.com"
GITHUB_API="https://api.github.com"
INSTALL_DIR="${HOME}/.knowledge/bin"

PATH_MARK_BEGIN='# BEGIN knowledge-managed PATH'
PATH_MARK_END='# END knowledge-managed PATH'

# The rc bodies below are SINGLE-QUOTED so $HOME and $PATH reach the
# user's dotfile verbatim and are expanded by THEIR shell, at THEIR
# startup. Double-quoting would expand them here and freeze this
# installer's transient PATH into the dotfile forever — which is why
# SC2016's advice is wrong in this specific place.
#
# The POSIX body is runtime-guarded, not merely append-guarded: sourcing
# it twice adds one entry, and sourcing it when the directory is already
# present adds none. That is what makes writing to more than one bash rc
# file safe, since ~/.bash_profile commonly sources ~/.bashrc.
# shellcheck disable=SC2016 # literal $HOME/$PATH belongs in the rc file verbatim
POSIX_PATH_LINE='case ":${PATH}:" in *":${HOME}/.knowledge/bin:"*) ;; *) export PATH="${HOME}/.knowledge/bin:${PATH}" ;; esac'
# shellcheck disable=SC2016 # literal $HOME/$PATH belongs in the rc file verbatim
FISH_PATH_LINE='if not contains "$HOME/.knowledge/bin" $PATH
    set -gx PATH "$HOME/.knowledge/bin" $PATH
end'
# shellcheck disable=SC2016 # literal $HOME/$PATH belongs in the rc file verbatim
POSIX_SESSION_LINE='export PATH="$HOME/.knowledge/bin:$PATH"'
# shellcheck disable=SC2016 # literal $HOME/$PATH belongs in the rc file verbatim
FISH_SESSION_LINE='set -gx PATH "$HOME/.knowledge/bin" $PATH'

HEADLESS=0
FORCE_SCRIPT=0
NO_MODIFY_PATH=0
BREW_MANAGED=0
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
	--no-modify-path) NO_MODIFY_PATH=1 ;;
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

path_hint() {
	echo "install.sh: add knowledge to your PATH:"
	echo "  $POSIX_SESSION_LINE"
}

# rc_display <path> — how to NAME an rc file in output. When the path is
# a symlink, name the file whose bytes actually change rather than the
# link: a user told "wrote ~/.zshrc" who finds a dotfile-repo symlink
# there cannot tell what was edited. There is no portable `readlink -f`
# on macOS, so the target is read out of `ls -ld`.
rc_display() {
	if [ ! -L "$1" ]; then
		printf '%s\n' "$1"
		return 0
	fi
	# shellcheck disable=SC2012 # reading a symlink target, not listing a directory: `readlink -f` is not portable to macOS
	rd_target="$(ls -ld "$1" 2>/dev/null | sed 's/.* -> //')"
	if [ -z "$rd_target" ] || [ "$rd_target" = "$1" ]; then
		printf '%s (a symlink whose target could not be resolved)\n' "$1"
		return 0
	fi
	case "$rd_target" in
	/*) printf '%s (via the symlink %s)\n' "$rd_target" "$1" ;;
	*) printf '%s/%s (via the symlink %s)\n' "$(dirname "$1")" "$rd_target" "$1" ;;
	esac
}

# ensure_path_entry — put ~/.knowledge/bin on PATH for FUTURE shells by
# appending a marker-guarded block to the rc file(s) of the user's
# default shell.
#
# FAIL-SOFT THROUGHOUT, and that is the point rather than a nicety: this
# script runs under `set -eu`, so an aborting write would leave the
# binaries installed but never reach `knowledge setup` — no config, no
# assets, no MCP registration, no service units. Every write and mkdir
# here tolerates failure, and the function returns 0 on every path it can
# reach.
#
# Concurrent installs are deliberately NOT locked. The marker grep and
# the append are not atomic, so a genuine race could write the block
# twice; the body is runtime-guarded, so a duplicated block still yields
# one PATH entry, and lockfile machinery is not worth its weight in a
# public bootstrap. Decided, not missed.
ensure_path_entry() {
	case ":${PATH}:" in
	*":$INSTALL_DIR:"*)
		echo "install.sh: $INSTALL_DIR is already on your PATH; no shell rc changes needed."
		return 0
		;;
	esac

	if [ "$NO_MODIFY_PATH" -eq 1 ] || [ -n "${KNOWLEDGE_INSTALL_NO_PATH:-}" ]; then
		echo "install.sh: leaving your shell rc files untouched (PATH opt-out)."
		path_hint
		return 0
	fi

	# ${SHELL:-} is mandatory, not stylistic. Under `set -u` a bare $SHELL
	# aborts the whole script when SHELL is unset — the state of docker
	# builds, CI runners and never-logged-in service accounts — and that
	# abort happens on dash, which IS /bin/sh on Debian and Ubuntu. Empty
	# and unset are treated alike.
	sh_path="${SHELL:-}"
	if [ -n "$sh_path" ]; then
		sh_name="${sh_path##*/}"
	elif [ "$os" = "darwin" ]; then
		sh_name="zsh"
	else
		sh_name="bash"
	fi

	rc_files=""
	body="$POSIX_PATH_LINE"
	session_line="$POSIX_SESSION_LINE"
	case "$sh_name" in
	*zsh)
		rc_files="${ZDOTDIR:-$HOME}/.zshrc"
		;;
	*bash)
		# Write to every bash rc that ALREADY exists, and invent one only
		# when none does. The .profile preference is a shadowing guard:
		# login bash reads the FIRST of .bash_profile/.bash_login/.profile,
		# so creating a .bash_profile beside an existing .profile would
		# silently stop that .profile being read. The darwin/linux split is
		# because macOS terminals start LOGIN bash (reads .bash_profile,
		# never .bashrc) while Linux terminals start non-login interactive
		# bash (reads .bashrc).
		for f in "$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.bash_login"; do
			if [ -f "$f" ]; then
				rc_files="$rc_files $f"
			fi
		done
		if [ -z "$rc_files" ]; then
			if [ -f "$HOME/.profile" ]; then
				rc_files="$HOME/.profile"
			elif [ "$os" = "darwin" ]; then
				rc_files="$HOME/.bash_profile"
			else
				rc_files="$HOME/.bashrc"
			fi
		fi
		;;
	*fish)
		# conf.d is fish's documented auto-sourced drop-in directory.
		# Chosen over fish_add_path, which mutates fish's universal-variable
		# store: that would require executing fish from this POSIX script
		# and would leave the state invisible to the rc file.
		body="$FISH_PATH_LINE"
		session_line="$FISH_SESSION_LINE"
		fish_dir="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d"
		if ! mkdir -p "$fish_dir" 2>/dev/null; then
			echo "install.sh: could not create $fish_dir — add this line yourself:" >&2
			echo "  $session_line" >&2
			return 0
		fi
		rc_files="$fish_dir/knowledge.fish"
		;;
	*)
		echo "install.sh: could not confidently detect your shell (SHELL=${sh_path:-unset}); leaving your shell rc files untouched."
		path_hint
		return 0
		;;
	esac

	# shellcheck disable=SC2086 # rc_files is a space-separated list we built; intentional word-split
	for rc in $rc_files; do
		if [ -f "$rc" ] && grep -Fq "$PATH_MARK_BEGIN" "$rc" 2>/dev/null; then
			echo "install.sh: PATH entry already present in $(rc_display "$rc")"
			continue
		fi
		# The redirections are ordered deliberately. `>>"$rc"` is attempted
		# FIRST, so the shell's own diagnostic — which names the errno
		# reason, Permission denied vs Is a directory, that our message does
		# not — still reaches the user; `2>/dev/null` covers only printf's
		# own stderr. Do NOT reorder to suppress it.
		if ! printf '\n%s\n%s\n%s\n' "$PATH_MARK_BEGIN" "$body" "$PATH_MARK_END" >>"$rc" 2>/dev/null; then
			echo "install.sh: could not write $rc — add this line yourself:" >&2
			echo "  $session_line" >&2
			continue
		fi
		echo "install.sh: added the knowledge PATH entry to $(rc_display "$rc")"
	done

	if [ "$BREW_MANAGED" -eq 1 ] && [ "$FORCE_SCRIPT" -eq 1 ] && command -v knowledge >/dev/null 2>&1; then
		echo "install.sh: a Homebrew-managed knowledge is also on your PATH. This entry PREPENDS $INSTALL_DIR, so a new shell will run the copy just installed there, not the Homebrew one."
	fi

	echo "install.sh: open a new terminal, or run this in the current one:"
	echo "  $session_line"
	return 0
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
	BREW_MANAGED=1
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
	# `knowledge --version` prints ONE TO THREE lines: the client always
	# first, then `server <ver>` when the daemon probe succeeds, then a
	# skew line when the two stamps differ. `head -1` is load-bearing — it
	# drops the lines that vary with whether a daemon is running — and the
	# comparison is EXACT because a substring match reports an installed
	# v1.2.30 as already being the target v1.2.3 and skips the download.
	ver_token="$(printf '%s\n' "$installed_ver" | head -1 | awk '{print $NF}')"
	case "$ver_token" in
	"$TAG")
		echo "install.sh: already up to date ($TAG); nothing to download."
		ensure_path_entry
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
# Best-effort sweep of orphans left by runs that died before their trap
# could fire — a SIGKILL, a power loss, a closed terminal — each holding a
# full extracted binary pair in the user's ~/.knowledge forever. This is
# not a lock: it may delete a CONCURRENT run's staging directory, which is
# safe because that run's own trap already tolerates the directory
# vanishing and the two-phase move aborts before touching
# ~/.knowledge/bin. The `|| true` is required because an unmatched glob
# exits nonzero under `set -e`.
rm -rf "${HOME}/.knowledge"/.staging.* 2>/dev/null || true
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

# --- (8) PATH: write the marked block to the right shell rc, unless the
# directory is already on PATH, the user opted out, or the shell could not
# be identified confidently ---
ensure_path_entry
# --- (9) handoff: exec the just-installed client with a CURATED forward
# — ALWAYS --no-self-update (install.sh already installed the current
# release; re-downloading would be redundant) plus only the setup-
# understood behavior flags actually received. Script-only flags
# (--force-script, --version, --no-modify-path) are STRIPPED. ---
# shellcheck disable=SC2086 # FORWARD is a curated flag list; intentional word-split
exec "$INSTALL_DIR/knowledge" setup --no-self-update ${FORWARD}
