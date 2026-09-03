#!/usr/bin/env bash
#
# Enforce the coverage gate.
#
# Go measures statements, not branches, so this is a weaker instrument than the
# line-and-branch gate the sibling Python libraries run. The compensating rule
# is that every uncovered statement must be named in coverage-exclusions.txt
# with a reason, and the list must stay short: an entry is a claim that a
# statement cannot be reached, not that testing it was inconvenient.
set -euo pipefail

cd "$(dirname "$0")/.."

EXCLUSIONS=$PWD/scripts/coverage-exclusions.txt
MODULES=${MODULES:-". httpx ginx mcpx"}

status=0
below=""
for m in $MODULES; do
  [ -d "$m" ] || continue
  # A module with no Go files of its own yet has nothing to measure.
  ls "$m"/*.go >/dev/null 2>&1 || continue
  profile="$PWD/cover-$(echo "$m" | tr -d './').out"
  (cd "$m" && go test -covermode=set -coverprofile="$profile" ./... >/dev/null)
  gaps=$(go tool cover -func="$profile" | grep -v '100.0%$' | grep -v '^total:' || true)
  [ -n "$gaps" ] && below="$below$gaps
"
  echo "$m: $(go tool cover -func="$profile" | tail -1 | awk '{print $NF}')"
done

if [ -z "$(echo "$below" | tr -d '[:space:]')" ]; then
  echo "every module at 100.0% of statements"
  exit 0
fi

while IFS= read -r line; do
  [ -z "$line" ] && continue
  location=$(basename "${line%%:*}")
  func=$(echo "$line" | awk '{print $2}')
  key="$location:$func"
  if grep -qF -- "$key" "$EXCLUSIONS" 2>/dev/null; then
    echo "allowed gap: $line"
  else
    echo "UNCOVERED: $line"
    status=1
  fi
done <<< "$below"

if [ "$status" -ne 0 ]; then
  echo
  echo "Every uncovered statement must be justified in $EXCLUSIONS."
  exit 1
fi
