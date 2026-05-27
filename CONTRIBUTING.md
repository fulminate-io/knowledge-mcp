# Contributing to Knowledge

Thanks for your interest. Knowledge is pre-1.0 and actively developed. Contributions of any size are welcome — bug reports, doc fixes, new features, design discussions.

## Ground rules

- Open an issue before starting non-trivial work so we can align on approach.
- Small, focused PRs. One concern per PR.
- Tests for new code. Update docs when behavior changes.
- The [Code of Conduct](CODE_OF_CONDUCT.md) applies. Harassment is not tolerated.

## Local development

Requirements:

- Go 1.25.7+
- CGO enabled (tree-sitter uses C bindings)

Build and test:

```bash
CGO_ENABLED=1 go build -o bin/knowledge .   # MCP stdio client
CGO_ENABLED=1 go test ./...
```

## Go code standards

These are enforced by `lefthook` pre-commit hooks:

- Functions under 80 lines (`funlen`).
- Files under 300 lines recommended, 500 lines hard limit.
- `golangci-lint` clean (config in `.golangci.yaml`).
- Never return `nil` when `err != nil` (`nilerr`). If you intentionally swallow an error, add `//nolint:nilerr` with an explanation.
- `store/` package is the base of the dependency pyramid — it must not import domain packages. See `CLAUDE.md` for the full architecture rules.

## Commit messages

Use conventional-commit style:

- `feat(scope): short description`
- `fix(scope): short description`
- `docs: short description`
- `refactor(scope): short description`
- `test(scope): short description`

Reference ticket IDs where relevant.

## Contributor License Agreement

Every pull request requires a signed Contributor License Agreement (CLA). On your first PR, the cla-assistant bot will comment with a link to sign.

- Individual contributors sign the [ICLA](docs/cla/ICLA.md).
- Contributors working on behalf of a company sign the [CCLA](docs/cla/CCLA.md).

**Why a CLA:** Apache 2.0 is the daily-driver license. The CLA exists so Knowledge can be defended against hyperscaler exploitation by relicensing if that ever becomes necessary. We have no current plans to relicense and will only do so as a last resort, after public discussion. The CLA does not change your ability to use Knowledge under Apache 2.0.

The CLA and its rationale are discussed in full in `docs/cla/`.

## Pull request flow

1. Fork + create a topic branch.
2. Make your change. Run `CGO_ENABLED=1 go test -p 4 ./...` locally.
3. Commit with a conventional-commit message. Push.
4. Open a PR against `main` using the [PR template](.github/PULL_REQUEST_TEMPLATE.md).
5. Sign the CLA when prompted by `cla-assistant`.
6. CI runs automatically. Address review feedback.
7. A maintainer merges when the PR is ready.

## Reporting security issues

Do not open a public issue for security vulnerabilities. Email `legal@fulminate.io` with the details. We'll respond within 72 hours.

## Questions

For general questions, open a GitHub Discussion (once enabled) or use the `question` issue template. For everything else, open an issue with the appropriate template.
