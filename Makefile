.PHONY: build test lint clean sync-assets

build: sync-assets
	CGO_ENABLED=1 go build -o bin/knowledge .

# Mirror .claude/{agents,skills} into the SHARED embed location used by
# internal/assets so both the install-claude-assets and
# install-codex-assets subcommands pick up the latest project
# agents/skills at build time. install-codex-assets translates agents
# .md→.toml at install time, so no separate codex sync is needed. The
# mirror destination is gitignored — canonical files live at
# .claude/{agents,skills}/. Idempotent.
sync-assets:
	@./scripts/sync-assets.sh

# test runs the full suite for the single module. -p 4 (CLAUDE.md:
# store/topology fixtures oversubscribe at the default -p ~15x).
test:
	CGO_ENABLED=1 go test -p 4 -count=1 ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/
