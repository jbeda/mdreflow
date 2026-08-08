#!/usr/bin/env bash
# Extract one release's notes from CHANGELOG.md for use as GitHub release
# notes: the section whose heading matches the given version, plus a
# "Full changelog" compare link against the previous tag when one exists.
#
# Usage: scripts/release-notes.sh vX.Y.Z [changelog-path]
set -euo pipefail

version="${1:?usage: release-notes.sh vX.Y.Z [changelog-path]}"
changelog="${2:-CHANGELOG.md}"

section=$(awk -v ver="$version" '
    /^## / {
        if (found) exit
        # Heading form: "## vX.Y.Z (date)" — match on the version token.
        if ($2 == ver) { found = 1; next }
    }
    found { print }
' "$changelog")

if [ -z "$section" ]; then
    echo "release-notes.sh: no section for $version in $changelog" >&2
    exit 1
fi

# Trim leading/trailing blank lines.
printf '%s\n' "$section" | awk 'NF {p=1} p' | tac | awk 'NF {p=1} p' | tac

prev=$(git describe --tags --abbrev=0 "${version}^" 2>/dev/null || true)
if [ -n "$prev" ]; then
    printf '\n**Full changelog**: https://github.com/jbeda/mdreflow/compare/%s...%s\n' "$prev" "$version"
fi
