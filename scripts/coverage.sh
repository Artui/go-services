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

PROFILE=${PROFILE:-cover.out}
EXCLUSIONS=scripts/coverage-exclusions.txt

go test -covermode=set -coverprofile="$PROFILE" ./... >/dev/null

below=$(go tool cover -func="$PROFILE" | grep -v '100.0%$' | grep -v '^total:' || true)

if [ -z "$below" ]; then
  echo "coverage: 100.0% of statements"
  exit 0
fi

status=0
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

go tool cover -func="$PROFILE" | tail -1
