#!/usr/bin/env bash
# Prepend SPDX-License-Identifier header to every .go file that lacks one.
# - If the file starts with //go:build or // +build, insert SPDX before the
#   build-tag block with a blank line separator (build-tag semantics require
#   a blank line before the package clause; we preserve that).
# - Otherwise, insert SPDX at the top followed by a blank line.
# - Idempotent: files that already have the SPDX line are left alone.
set -euo pipefail

HEADER='// SPDX-License-Identifier: Apache-2.0'

count_skipped=0
count_added=0

while IFS= read -r -d '' file; do
    if head -n 1 "$file" | grep -qxF "$HEADER"; then
        count_skipped=$((count_skipped + 1))
        continue
    fi

    tmp="$(mktemp)"
    printf '%s\n\n' "$HEADER" > "$tmp"
    cat "$file" >> "$tmp"
    mv "$tmp" "$file"
    count_added=$((count_added + 1))
done < <(find . -name '*.go' -type f -not -path './vendor/*' -not -path './bin/*' -print0)

echo "Added SPDX header to $count_added files"
echo "Skipped $count_skipped files (already had header)"
