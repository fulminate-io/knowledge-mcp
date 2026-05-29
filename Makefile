.PHONY: build test lint clean sync-claude-assets

build: sync-claude-assets
	CGO_ENABLED=1 go build -o bin/knowledge .

# Mirror .claude/{agents,skills} into the embed location used by
# internal/claudeassets so the install-claude-assets subcommand picks up
# the latest project agents/skills at build time. The mirror destination
# is gitignored — canonical files live at .claude/{agents,skills}/.
# Idempotent.
sync-claude-assets:
	@./scripts/sync-claude-assets.sh

# test runs the full suite for the single module. -p 4 (CLAUDE.md:
# store/topology fixtures oversubscribe at the default -p ~15x).
test:
	CGO_ENABLED=1 go test -p 4 -count=1 ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/
