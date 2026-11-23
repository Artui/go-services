#!/usr/bin/env bash
#
# Fixtures for scripts/release.sh and scripts/release-detect.sh.
#
# Each case builds a throwaway repository with a bare "origin" beside it, so no
# tag ever reaches a real remote, and serves the proxy and sum database answers
# from a local HTTP server. Every refusal case starts from a state the happy
# path accepts and breaks exactly one thing, so the check under test is the one
# that answers.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
release=${RELEASE_SCRIPT:-$here/release.sh}
detect=$here/release-detect.sh

root=$(mktemp -d)
server_pid=
cleanup() {
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  rm -rf "$root"
}
trap cleanup EXIT

# Isolate git from the machine's config: no signing, hooks or default branch.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.invalid
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.invalid

www=$root/www
mkdir -p "$www"
python3 - "$www" "$root/port" <<'EOF' &
import functools, http.server, sys
class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass
handler = functools.partial(Quiet, directory=sys.argv[1])
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
open(sys.argv[2], "w").write(str(server.server_address[1]))
server.serve_forever()
EOF
server_pid=$!
for _ in $(seq 50); do [ -s "$root/port" ] && break; sleep 0.1; done
[ -s "$root/port" ] || { echo "the fixture HTTP server did not start" >&2; exit 1; }
base=http://127.0.0.1:$(cat "$root/port")
export GOPROXY_URL=$base/proxy SUMDB_URL=$base/sumdb

failures=0
case_dir=

# fixture: a repo whose main the happy path accepts for aguix v0.5.0 and the
# kernel v0.3.0, with kernel tags v0.1.0 and v0.2.0 on the remote.
fixture() {
  rm -rf "$www/proxy" "$www/sumdb"
  case_dir=$(mktemp -d "$root/case.XXXXXX")
  git init -q --bare -b main "$case_dir/origin.git"
  git init -q -b main "$case_dir/work"
  cd "$case_dir/work"
  git remote add origin "$case_dir/origin.git"
  printf 'module github.com/Artui/go-services\n\ngo 1.24\n' >go.mod
  mkdir aguix conformance
  printf 'module github.com/Artui/go-services/aguix\n\ngo 1.24\n\nrequire github.com/Artui/go-services v0.2.0\n' >aguix/go.mod
  printf 'module github.com/Artui/go-services/conformance\n\ngo 1.24\n\nrequire (\n\tgithub.com/Artui/go-services v0.2.0\n)\n\nreplace github.com/Artui/go-services => ../\n' >conformance/go.mod
  cat >CHANGELOG.md <<'EOF'
# Changelog

## Adapters

`aguix/v0.4.0` did the earlier thing.

`aguix/v0.5.0` does a new thing. `conformance/v0.1.0` is named only to reach
the replace check.

## [Unreleased]

- The `aguix/v0.6.0` paragraph is still to come.

## [0.3.0] - 2026-01-02

## [0.2.0] - 2026-01-01
EOF
  git add -A
  git commit -q -m one
  git tag -a v0.1.0 -m v0.1.0
  git tag -a v0.2.0 -m v0.2.0
  git push -q origin main v0.1.0 v0.2.0
  git tag -d v0.1.0 v0.2.0 >/dev/null
}

serve() { # serve PATH [BODY]: make the fake proxy or sum database answer 200
  mkdir -p "$(dirname "$www/$1")"
  printf '%s\n' "${2:-ok}" >"$www/$1"
}

# expect NAME CODE PATTERN CMD...: run CMD, require its exit code and that its
# combined output matches PATTERN.
expect() {
  local name=$1 want=$2 pattern=$3 out code
  shift 3
  set +e
  out=$("$@" 2>&1)
  code=$?
  set -e
  if [ "$code" != "$want" ] || ! grep -qE -- "$pattern" <<<"$out"; then
    echo "FAIL $name: exit $code (want $want), output:"
    sed 's/^/    /' <<<"$out"
    failures=$((failures + 1))
  else
    echo "ok   $name"
  fi
}

check() { # check NAME CONDITION...: a plain assertion on the fixture's state
  local name=$1
  shift
  if "$@"; then echo "ok   $name"; else
    echo "FAIL $name"
    failures=$((failures + 1))
  fi
}

remote_has() { [ -n "$(git ls-remote origin "refs/tags/$1")" ]; }
remote_lacks() { ! remote_has "$1"; }
local_has() { git rev-parse -q --verify "refs/tags/$1" >/dev/null; }

# The happy path: a local annotated tag on main, nothing pushed, and the push
# command printed.
fixture
expect "tag: creates a local tag" 0 'git push origin refs/tags/aguix/v0.5.0' "$release" tag aguix v0.5.0
check "tag: the tag is annotated and on main" [ "$(git cat-file -t aguix/v0.5.0)" = tag ]
check "tag: without PUSH nothing reaches the remote" remote_lacks aguix/v0.5.0

fixture
expect "tag: PUSH=1 pushes" 0 'pushed aguix/v0.5.0' env PUSH=1 "$release" tag aguix v0.5.0
check "tag: PUSH=1 put the tag on the remote" remote_has aguix/v0.5.0

fixture
expect "tag: the kernel takes a bare tag" 0 'created v0.3.0' "$release" tag kernel v0.3.0
check "tag: the kernel tag is bare" local_has v0.3.0

# Refusals.
fixture
touch stray
expect "refuse: an untracked file" 1 'working tree is not clean' "$release" tag aguix v0.5.0

fixture
echo change >>go.mod
expect "refuse: a modified file" 1 'working tree is not clean' "$release" tag aguix v0.5.0

fixture
git clone -q "$case_dir/origin.git" "$case_dir/other"
(cd "$case_dir/other" && git commit -q --allow-empty -m two && git push -q origin main)
expect "refuse: HEAD behind origin/main" 1 'HEAD is .* but the commit to tag is' "$release" tag aguix v0.5.0

fixture
git commit -q --allow-empty -m unpushed
expect "refuse: HEAD ahead of origin/main" 1 'HEAD is .* but the commit to tag is' "$release" tag aguix v0.5.0

fixture
git checkout -q -b side
git commit -q --allow-empty -m side
expect "refuse: a SHA not on main" 1 'is not on origin/main' env SHA="$(git rev-parse HEAD)" "$release" tag aguix v0.5.0

fixture
git commit -q --allow-empty -m two && git push -q origin main
first=$(git rev-parse HEAD~1)
git checkout -q "$first"
expect "tag: a SHA on main, checked out" 0 "created aguix/v0.5.0 at $first" env SHA="$first" "$release" tag aguix v0.5.0

fixture
git tag -a aguix/v0.5.0 -m x && git push -q origin aguix/v0.5.0 && git tag -d aguix/v0.5.0 >/dev/null
expect "refuse: the tag is on the remote" 1 'already exists on origin' "$release" tag aguix v0.5.0

fixture
git tag -a aguix/v0.5.0 -m x
expect "refuse: the tag exists locally" 1 'already exists locally' "$release" tag aguix v0.5.0

fixture
serve 'sumdb/lookup/github.com/!artui/go-services/aguix@v0.5.0'
expect "refuse: the version is in the sum database" 1 'already in the sum database' "$release" tag aguix v0.5.0

fixture
expect "refuse: the sum database does not answer" 1 'could not ask the sum database' \
  env SUMDB_URL=http://127.0.0.1:9 "$release" tag aguix v0.5.0

fixture
expect "refuse: no Adapters paragraph" 1 'does not name `aguix/v0.7.0`' "$release" tag aguix v0.7.0

fixture
expect "refuse: named only under [Unreleased]" 1 'does not name `aguix/v0.6.0`' "$release" tag aguix v0.6.0

fixture
expect "refuse: no kernel heading" 1 "no '## \\[0.4.0\\]' heading" "$release" tag kernel v0.4.0

fixture
git tag -a v0.3.0 -m v0.3.0 && git push -q origin v0.3.0 && git tag -d v0.3.0 >/dev/null
expect "refuse: the kernel floor is not the newest tag" 1 'floors the kernel at v0.2.0 but the newest kernel tag is v0.3.0' \
  "$release" tag aguix v0.5.0

fixture
expect "refuse: a module with a replace" 1 'carries a replace directive' "$release" tag conformance v0.1.0

fixture
expect "refuse: a directory with no go.mod" 1 'has no go.mod' "$release" tag nothere v0.1.0

fixture
sed -i.bak 's#^module .*#module example.com/elsewhere#' aguix/go.mod && rm aguix/go.mod.bak
git commit -q -am elsewhere && git push -q origin main
expect "refuse: a go.mod for another module" 1 'does not declare module github.com/Artui/go-services/aguix' \
  "$release" tag aguix v0.5.0

fixture
git push -q origin :refs/tags/v0.1.0 :refs/tags/v0.2.0
expect "refuse: no kernel tag on the remote" 1 'found no kernel tag' "$release" tag aguix v0.5.0

fixture
printf 'module github.com/Artui/go-services/aguix\n\ngo 1.24\n\nrequire (\n\tgithub.com/Artui/go-services v0.1.0\n\tgithub.com/other/thing v0.2.0 // indirect\n)\n' >aguix/go.mod
git commit -q -am block && git push -q origin main
expect "refuse: a stale floor in a require block" 1 'floors the kernel at v0.1.0 but the newest kernel tag is v0.2.0' \
  "$release" tag aguix v0.5.0

fixture
for v in v0.9.0 v0.10.0; do git tag -a "$v" -m "$v" && git push -q origin "$v" && git tag -d "$v" >/dev/null; done
sed -i.bak 's/v0.2.0$/v0.9.0/' aguix/go.mod && rm aguix/go.mod.bak
git commit -q -am nine && git push -q origin main
expect "refuse: v0.9.0 is not the newest when v0.10.0 exists" 1 'floors the kernel at v0.9.0 but the newest kernel tag is v0.10.0' \
  "$release" tag aguix v0.5.0

fixture
expect "refuse: a remote that cannot be read" 1 'could not fetch main' env REMOTE="$case_dir/missing.git" "$release" tag aguix v0.5.0
expect "refuse: MODULE with a trailing slash" 1 'MODULE is' "$release" tag aguix/ v0.5.0
expect "refuse: MODULE with a leading ./" 1 'MODULE is' "$release" tag ./aguix v0.5.0

fixture
expect "refuse: a v2 version" 1 'VERSION must be' "$release" tag aguix v2.0.0
expect "refuse: a version without the v" 1 'VERSION must be' "$release" tag aguix 0.5.0

# release-verify.
fixture
sha=$(git rev-parse HEAD)
expect "verify: not on the remote" 1 'not yet: aguix/v0.5.0 is not on origin' "$release" verify aguix v0.5.0
PUSH=1 "$release" tag aguix v0.5.0 >/dev/null 2>&1
expect "verify: pushed, not yet served" 1 'not yet: the module proxy' "$release" verify aguix v0.5.0
serve 'proxy/github.com/!artui/go-services/aguix/@v/v0.5.0.info' "{\"Version\":\"v0.5.0\",\"Origin\":{\"Hash\":\"$sha\"}}"
expect "verify: served by the proxy, not in the sum database" 1 'not yet: the sum database' "$release" verify aguix v0.5.0
serve 'sumdb/lookup/github.com/!artui/go-services/aguix@v0.5.0'
expect "verify: served by both" 0 'ok: the sum database records' "$release" verify aguix v0.5.0
mv "$www/proxy" "$www/proxy.off"
expect "verify: in the sum database, not served by the proxy" 1 'not yet: the module proxy' "$release" verify aguix v0.5.0
mv "$www/proxy.off" "$www/proxy"
git tag -d aguix/v0.5.0 >/dev/null
expect "verify: with no local tag, from the remote" 0 'ok: aguix/v0.5.0 on origin points at' "$release" verify aguix v0.5.0
serve 'proxy/github.com/!artui/go-services/aguix/@v/v0.5.0.info' '{"Version":"v0.5.0","Origin":{"Hash":"0000000000000000000000000000000000000000"}}'
expect "verify: the proxy serves another commit" 1 'mismatch: the module proxy serves' "$release" verify aguix v0.5.0

fixture
git commit -q --allow-empty -m two && git push -q origin main
other=$(git rev-parse HEAD)
git tag -a aguix/v0.5.0 -m x HEAD~1 && git push -q origin aguix/v0.5.0
expect "verify: the remote tag is not on SHA" 1 'mismatch: aguix/v0.5.0 on origin points at' env SHA="$other" "$release" verify aguix v0.5.0

fixture
git checkout -q -b side && git commit -q --allow-empty -m side
git tag -a aguix/v0.5.0 -m x && git push -q origin aguix/v0.5.0 && git tag -d aguix/v0.5.0 >/dev/null
expect "verify: the remote tag is not on main" 1 'points at .*, which is not on main' "$release" verify aguix v0.5.0

# release-detect.
fixture
before=$(git rev-parse HEAD)
expect "detect: an unchanged changelog names nothing" 0 '^$' "$detect" "$before" "$before"
sed -i.bak 's/^`aguix\/v0.5.0` does/`aguix\/v0.5.1` and `aguix\/v0.4.0` do/' CHANGELOG.md
sed -i.bak 's/^## \[Unreleased\]/## [Unreleased]\n\n- `httpx\/v9.9.9` in passing./' CHANGELOG.md
sed -i.bak 's/^## \[0.3.0\]/## [0.4.0] - 2026-01-03\n\n## [0.3.0]/' CHANGELOG.md
rm CHANGELOG.md.bak
git commit -q -am promote
# Exact output: grep would read a two-line pattern as two alternatives.
check "detect: a new heading and a new Adapters token, kernel first" \
  [ "$("$detect" "$before" HEAD)" = "$(printf 'kernel v0.4.0\naguix v0.5.1')" ]
expect "detect: nothing from [Unreleased]" 0 '' bash -c "! \"$detect\" $before HEAD | grep -q httpx"
expect "detect: a token already present is not new" 0 '' bash -c "! \"$detect\" $before HEAD | grep -q 'aguix v0.4.0'"

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures release fixture(s) failed"
  exit 1
fi
echo "every release fixture passed"
