#!/usr/bin/env bash
#
# Print the releases a change to CHANGELOG.md names for the first time, one
# "MODULE VERSION" per line, kernel first, in the form scripts/release.sh takes.
#
#   scripts/release-detect.sh BEFORE AFTER
#
# A release is named in two places only: a new "## [X.Y.Z]" heading is a kernel
# version, and a new `<dir>/vX.Y.Z` token in the "## Adapters" section is a
# module's. [Unreleased] is never read, so an entry that mentions a version in
# passing tags nothing. A token already present at BEFORE is not new, however
# often a later paragraph repeats it.
set -euo pipefail

[ $# -eq 2 ] || {
  echo "usage: $0 BEFORE AFTER" >&2
  exit 2
}

changelog_at() {
  git show "$1:CHANGELOG.md" 2>/dev/null || true
}

kernel_versions() {
  sed -nE 's/^## \[([0-9]+\.[0-9]+\.[0-9]+)\].*/v\1/p' | sort -u
}

module_versions() {
  awk '/^## /{ in_section = ($0 == "## Adapters"); next } in_section' |
    grep -oE '`[a-z0-9][a-z0-9_/-]*/v[0-9]+\.[0-9]+\.[0-9]+`' |
    tr -d '`' | sed -E 's#/(v[0-9]+\.[0-9]+\.[0-9]+)$# \1#' | sort -u || true
}

before=$(changelog_at "$1")
after=$(changelog_at "$2")

comm -13 <(kernel_versions <<<"$before") <(kernel_versions <<<"$after") | sed 's/^/kernel /'
comm -13 <(module_versions <<<"$before") <(module_versions <<<"$after")
