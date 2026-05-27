# CLA Assistant Setup (one-time, manual)

This file documents the one-time setup required to activate `cla-assistant.io` against the Knowledge repository. Run these steps after the LICENSE, ICLA, CCLA, and `.github/cla.json` files have landed on `main`.

## Prerequisites

- Admin access to `github.com/fulminate-io/knowledge-mcp`
- A GitHub account (personal or organization) authorized to install apps on the Fulminate organization
- The repository must be public (or at least accessible to contributors)

## Steps

1. **Visit** https://cla-assistant.io and sign in with GitHub.

2. **Authorize** the cla-assistant GitHub App when prompted. The app needs read/write access to:
   - Pull requests (to post CLA status comments)
   - Statuses (to mark PRs as CLA-required)
   - Repository metadata

3. **Activate** cla-assistant for `fulminate-io/knowledge-mcp`:
   - Select the repository from the list
   - Link it to the ICLA gist or remote file: paste the canonical URL `https://github.com/fulminate-io/knowledge-mcp/blob/main/docs/cla/ICLA.md`
   - Configure whitelist-able GitHub usernames if desired (e.g., `dependabot[bot]`, `claude[bot]`)

4. **Test** with a dummy PR:
   - From a fork (not a maintainer account), open a trivial PR (e.g., fix a typo)
   - The cla-assistant bot should comment with the signing link
   - Sign the CLA via the bot link; confirm the PR status updates to green

5. **Update** the README or relevant landing page to link to the CLA sign-in URL so first-time contributors have direct access.

## Corporate CLA flow

For corporate contributors:

1. Authorized company officer emails `legal@fulminate.io` to initiate a CCLA
2. Fulminate sends the CCLA PDF for signature (out-of-band, e.g. DocuSign)
3. On signed-and-returned CCLA, Fulminate adds the corporation's Designated Employees to the cla-assistant whitelist via the admin UI
4. Those employees' pull requests are then pre-approved without per-PR signing

## Maintenance

- **Adding a new Designated Employee (CCLA):** admin adds their GitHub username to the cla-assistant whitelist for this repo.
- **Removing a Designated Employee:** admin removes them from the whitelist. Existing signed contributions are not affected.
- **Changing the ICLA/CCLA text:** update the file in `docs/cla/`, then update the URL reference in cla-assistant. Existing signatures are retained; new signatures use the updated text.

## Security

Reports of security issues with signed CLAs, bot behavior, or signature forgery should go to `legal@fulminate.io`. Do not open a public issue.
