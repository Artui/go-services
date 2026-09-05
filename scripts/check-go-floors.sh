#!/usr/bin/env bash
#
# Assert each module's declared Go floor.
#
# Go raises the `go` directive on its own when a dependency needs a newer one,
# and it then downloads that toolchain rather than failing -- so a floor can
# climb without any build breaking and without anyone deciding to. The CI matrix
# does not catch it either: a leg named for an old Go silently fetches the newer
# toolchain for whichever module asks.
#
# Raising a floor is allowed. Raising it by accident is what this refuses.
set -euo pipefail
cd "$(dirname "$0")/.."

# module:expected-floor, and why it is not lower.
EXPECTED=(
  ".:1.24"      # encoding/json's omitzero, which Optional[T] needs
  "httpx:1.24"  # the kernel's floor; net/http path values arrived in 1.22
  "ginx:1.26.0" # x/crypto and quic-go, pulled up to clear Gin's advisories
  "mcpx:1.25.0" # the MCP SDK's own go.mod
  "adkx:1.26.6" # adk-go's own go.mod, the highest floor in the repo
  "aguix:1.24"  # the kernel's floor; it depends on nothing else
  "conformance:1.26.6" # the highest of the modules it drives, which is adkx
  "example:1.26.6"     # the same: it mounts all four adapters at once
)

status=0
for entry in "${EXPECTED[@]}"; do
  module=${entry%%:*}
  want=${entry#*:}
  got=$(awk '/^go /{print $2; exit}' "$module/go.mod")
  if [ "$got" != "$want" ]; then
    echo "$module declares go $got, expected $want"
    echo "  If the raise is deliberate, update scripts/check-go-floors.sh and say"
    echo "  in the same commit which dependency forced it."
    status=1
  fi
done

[ "$status" -eq 0 ] && echo "every module is at its declared Go floor"
exit "$status"
