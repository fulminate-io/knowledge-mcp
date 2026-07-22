# Installing knowledge on native Windows

The one-line `curl … | sh` installer and Homebrew both target macOS and
Linux. On **native Windows** there is no installer script — install the two
binaries by hand, then run `knowledge setup`. (If you use **WSL**, don't
follow this page: run the standard one-liner inside your Linux distro
instead.)

There is intentionally **no PowerShell installer** (`install.ps1`); the
manual steps below are the supported Windows path.

## 1. Download the release assets

From the latest release on the public repo —
`https://github.com/fulminate-io/knowledge-mcp/releases/latest` — download
these three files (they live under `releases/download/<tag>/`):

- `knowledge-windows-amd64.zip` — the client
- `knowledge-server-windows-amd64.zip` — the graph server
- `checksums.txt` — the SHA-256 manifest

## 2. Verify the checksums

In PowerShell, confirm each `.zip`'s SHA-256 matches its line in
`checksums.txt` before trusting the download:

```powershell
Get-FileHash knowledge-windows-amd64.zip -Algorithm SHA256
Get-FileHash knowledge-server-windows-amd64.zip -Algorithm SHA256
Get-Content checksums.txt
```

The `Hash` value (lower-cased) must equal the hash listed next to the
matching filename in `checksums.txt`. If either differs, delete the file
and download it again — do not proceed with a mismatched archive.

## 3. Extract and place both binaries on PATH

Each zip contains a single executable (`knowledge.exe` and
`knowledge-server.exe`). Extract both into one directory and add that
directory to your `PATH`, for example:

```powershell
$dest = "$env:USERPROFILE\.knowledge\bin"
New-Item -ItemType Directory -Force -Path $dest | Out-Null
Expand-Archive -Force knowledge-windows-amd64.zip -DestinationPath $dest
Expand-Archive -Force knowledge-server-windows-amd64.zip -DestinationPath $dest

# Add to PATH for the current user (new terminals pick it up):
[Environment]::SetEnvironmentVariable(
  "Path", "$dest;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

Open a **new** terminal so the updated `PATH` takes effect, then confirm:

```powershell
knowledge --version
knowledge-server --version
```

## 4. Run setup

```powershell
knowledge setup
```

`knowledge setup` writes a starter config to `~\.knowledge\config`,
registers the Claude/Codex assets when those CLIs are on your `PATH`, and
starts the daemons for this session. Windows has **no boot-persistence
service** — the daemon runs for the current session; re-run
`knowledge setup` (or start the daemon manually) after a reboot.

To update later, repeat steps 1–3 with the newer release and re-run
`knowledge setup`.
