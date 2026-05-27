# Security Policy

## Reporting a Vulnerability

Do **not** open a public GitHub issue for security vulnerabilities.

Email `legal@fulminate.io` with:

- A description of the vulnerability
- Steps to reproduce (or a proof-of-concept)
- Affected versions, if known
- Your name / handle for credit (optional)

We respond to confirmed reports within 72 hours and aim to ship a fix or coordinated disclosure within 30 days for high-severity issues.

## Supported Versions

The latest release on the `main` branch receives security fixes. Older releases are not patched.

## Scope

In scope:

- The `knowledge` binaries built from this repository.
- Any code under `cmd/` or `gen/` paths.

Out of scope:

- Third-party dependencies — please report those upstream.
- The Fulminate Cloud service (a separate product) — report cloud issues to `security@fulminate.io`.

## Disclosure

We credit reporters in release notes unless they request otherwise. We do not currently run a paid bug bounty program.
