<!--
Thanks for opening a pull request. A few checks before the review:
-->

## Summary

<!-- One or two sentences on what this PR does and why. -->

## Related

<!-- Link to related issues, tickets, or prior PRs. -->

- Closes #

## Changes

<!-- Bullet list of the material changes in this PR. -->

- 

## Test plan

<!-- How did you verify this works? Commands run, cases covered. -->

- [ ] `CGO_ENABLED=1 go build ./...` passes
- [ ] `CGO_ENABLED=1 go test -p 4 ./...` passes
- [ ] New tests added where appropriate
- [ ] Documentation updated (CLAUDE.md, README.md, inline comments) where behavior changed

## Checklist

- [ ] Conventional-commit message format
- [ ] Files under 300 lines; functions under 80 lines (per `CLAUDE.md` rules)
- [ ] No `nil` returns when `err != nil`
- [ ] CLA signed on first PR (cla-assistant bot will prompt)
