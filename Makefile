.PHONY: build test lint clean sync-claude-assets

build: sync-claude-assets
	CGO_ENABLED=1 go build -o bin/knowledge ./cmd/knowledge

# Mirror .claude/{agents,skills} into the embed location used by
# cmd/knowledge/internal/claudeassets so the install-claude-assets
# subcommand picks up the latest project agents/skills at build time.
# The mirror destination is gitignored — canonical files live at
# .claude/{agents,skills}/. Idempotent.
sync-claude-assets:
	@./scripts/sync-claude-assets.sh

# test runs the full suite across all three workspace modules (contract,
# client, server). `go test ./...` is module-scoped even under go.work — it
# only covers the module of the current directory — so each module needs its
# own run: the root run covers the contract module; the client run covers
# cmd/knowledge; the server run covers cmd/knowledge-server with -tags
# internal so the //go:build internal cloud tree is exercised (that tag is a
# superset, so it also runs the server's tag-free tests). The plain -tags
# internal unit run does NOT require Docker — the cloud integration tests are
# additionally gated //go:build integration && internal.
# -p 4 on every run (CLAUDE.md: store/topology fixtures oversubscribe at
# the default -p ~15x).
test:
	CGO_ENABLED=1 go test -p 4 -count=1 ./...
	cd cmd/knowledge && CGO_ENABLED=1 go test -p 4 -count=1 ./...

# golangci-lint v2 is module-scoped — it does not cross module boundaries,
# so it runs once per module dir. The server pass adds --build-tags internal
# so the cloud tree is linted. The root .golangci.yml is discovered upward
# from each module dir.
lint:
	golangci-lint run
	cd cmd/knowledge && golangci-lint run

clean:
	rm -rf bin/
