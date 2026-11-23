#!/usr/bin/env bash
#
# Cut or verify one module's release tag.
#
#   scripts/release.sh tag    MODULE VERSION   (make release)
#   scripts/release.sh verify MODULE VERSION   (make release-verify)
#
# A tag is the whole publish here: the moment it is pushed, the module proxy and
# sum.golang.org record it, and it can never be re-pointed. So `tag` refuses
# everything it can check before the point of no return, and creates the tag
# locally; it pushes only with PUSH=1. The tag-on-merge workflow calls this same
# script, so a laptop and CI run the same checks.
#
# MODULE=kernel tags the root module as a bare vX.Y.Z; any other MODULE is a
# module directory and tags <dir>/vX.Y.Z.
#
# Environment:
#   SHA        tag this commit instead of origin/main (it must be on main, and
#              checked out)
#   PUSH=1     push the tag after creating it
#   REMOTE     the remote to read and push (default: origin)
#   GOPROXY_URL, SUMDB_URL  lookup endpoints, overridable so the fixtures can
#              serve their own answers
#
# Everything is read from the commit being tagged (`git show <sha>:path`), never
# from the working tree, so what is checked is what is published.
set -euo pipefail

REMOTE=${REMOTE:-origin}
GOPROXY_URL=${GOPROXY_URL:-https://proxy.golang.org}
SUMDB_URL=${SUMDB_URL:-https://sum.golang.org}
MODULE_ROOT=github.com/Artui/go-services
KERNEL_FLOOR_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'

die() {
  echo "release: $*" >&2
  exit 1
}

usage() {
  die "usage: $0 tag|verify MODULE VERSION (MODULE=kernel or a module directory)"
}

[ $# -eq 3 ] || usage
action=$1
module=$2
version=$3
[ -n "$module" ] || usage
[ -n "$version" ] || usage

# A v2+ tag on a path without a /vN suffix resolves as +incompatible, which is
# never what a release here means.
[[ "$version" =~ ^v[01]\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
  die "VERSION must be v0.Y.Z or v1.Y.Z, got '$version'"

if [ "$module" = kernel ]; then
  dir=.
  tag=$version
  path=$MODULE_ROOT
else
  case "$module" in
  . | ./* | /* | *..* | */) die "MODULE is 'kernel' or a module directory such as aguix, got '$module'" ;;
  esac
  dir=$module
  tag=$module/$version
  path=$MODULE_ROOT/$module
fi

# The proxy and the sum database spell an upper-case letter as '!' plus its
# lower case, so github.com/Artui becomes github.com/!artui.
escaped=
for ((i = 0; i < ${#path}; i++)); do
  c=${path:i:1}
  if [[ "$c" == [[:upper:]] ]]; then
    escaped+="!$(tr '[:upper:]' '[:lower:]' <<<"$c")"
  else
    escaped+=$c
  fi
done

cd "$(git rev-parse --show-toplevel)"

# status_of URL prints the HTTP status, or 000 when nothing answered.
status_of() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$1" || true
}

remote_commit() {
  git ls-remote "$REMOTE" "refs/tags/$1^{}" "refs/tags/$1" |
    awk -v peeled="refs/tags/$1^{}" -v plain="refs/tags/$1" '
      $2 == peeled { p = $1 } $2 == plain { l = $1 }
      END { if (p != "") print p; else if (l != "") print l }'
}

fetch_main() {
  git fetch --quiet "$REMOTE" "+refs/heads/main:refs/remotes/$REMOTE/main" ||
    die "could not fetch main from $REMOTE"
}

cmd_tag() {
  fetch_main
  local main head target
  main=$(git rev-parse "refs/remotes/$REMOTE/main")
  head=$(git rev-parse HEAD)

  if [ -n "${SHA:-}" ]; then
    target=$(git rev-parse --verify --quiet "$SHA^{commit}") || die "SHA '$SHA' is not a commit here"
    git merge-base --is-ancestor "$target" "$main" ||
      die "SHA $target is not on $REMOTE/main"
  else
    target=$main
  fi
  [ "$head" = "$target" ] ||
    die "HEAD is $head but the commit to tag is $target; check it out (git checkout $target) and run again"

  [ -z "$(git status --porcelain)" ] ||
    die "the working tree is not clean; commit or remove these first:
$(git status --short)"

  # A tag that exists anywhere is final. The remote is asked directly rather
  # than through a fetched copy, so a stale clone cannot hide one.
  [ -z "$(remote_commit "$tag")" ] || die "$tag already exists on $REMOTE"
  ! git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null ||
    die "$tag already exists locally; delete it (git tag -d $tag) if it was never pushed"
  # A tag once pushed and deleted is still in the sum database, so the same
  # version can never be cut again. Only a definite not-found means absent.
  local code
  code=$(status_of "$SUMDB_URL/lookup/$escaped@$version")
  case "$code" in
  404 | 410) ;;
  200) die "$path@$version is already in the sum database: that version was published once and cannot be reused" ;;
  *) die "could not ask the sum database about $path@$version (HTTP $code); refusing rather than guessing" ;;
  esac

  local gomod
  gomod=$(git show "$target:$dir/go.mod" 2>/dev/null) || die "$dir has no go.mod at $target"
  grep -qE "^module $path\$" <<<"$gomod" || die "$dir/go.mod does not declare module $path"
  # conformance and example point replace at the working tree and are never
  # tagged; a published go.mod with a replace means nothing to a consumer.
  ! grep -qE '^replace([[:space:]]|$)' <<<"$gomod" ||
    die "$dir/go.mod carries a replace directive; modules with one are never tagged"

  local changelog
  changelog=$(git show "$target:CHANGELOG.md" 2>/dev/null) || die "no CHANGELOG.md at $target"
  if [ "$module" = kernel ]; then
    local bare=${version#v}
    grep -qE "^## \\[${bare//./\\.}\\]" <<<"$changelog" ||
      die "CHANGELOG.md at $target has no '## [${version#v}]' heading; promote it in its own PR first"
  else
    # The Adapters section, from its heading to the next level-two heading.
    awk '/^## /{ in_section = ($0 == "## Adapters") ; next } in_section' <<<"$changelog" |
      grep -qF "\`$tag\`" ||
      die "the Adapters section of CHANGELOG.md at $target does not name \`$tag\`; promote its paragraph in its own PR first"

    local newest floor
    newest=$(git ls-remote --tags --refs "$REMOTE" 'v*' | awk '{sub("refs/tags/", "", $2); print $2}' |
      { grep -E "$KERNEL_FLOOR_RE" || true; } | sort -V | tail -n 1)
    [ -n "$newest" ] || die "found no kernel tag on $REMOTE"
    floor=$(awk -v root="$MODULE_ROOT" '
      $1 == "require" && $2 == root { print $3; exit }
      $1 == root && NF >= 2 { print $2; exit }' <<<"$gomod")
    [ -n "$floor" ] || die "$dir/go.mod does not require $MODULE_ROOT"
    [ "$floor" = "$newest" ] ||
      die "$dir/go.mod floors the kernel at $floor but the newest kernel tag is $newest; every module floors on the newest kernel"
  fi

  git tag -a "$tag" -m "$tag" "$target"
  echo "created $tag at $target"
  if [ "${PUSH:-}" = 1 ]; then
    git push "$REMOTE" "refs/tags/$tag"
    echo "pushed $tag; run: make release-verify MODULE=$module VERSION=$version"
  else
    echo "not pushed (PUSH is unset). To publish, which cannot be undone:"
    echo "  git push $REMOTE refs/tags/$tag"
    echo "then: make release-verify MODULE=$module VERSION=$version"
  fi
}

cmd_verify() {
  local remote expected status=0
  remote=$(remote_commit "$tag")
  if [ -z "$remote" ]; then
    echo "not yet: $tag is not on $REMOTE"
    exit 1
  fi
  if [ -n "${SHA:-}" ]; then
    expected=$(git rev-parse --verify --quiet "$SHA^{commit}") || die "SHA '$SHA' is not a commit here"
  elif git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
    expected=$(git rev-parse "refs/tags/$tag^{commit}")
  else
    fetch_main
    git merge-base --is-ancestor "$remote" "refs/remotes/$REMOTE/main" 2>/dev/null ||
      die "$tag on $REMOTE points at $remote, which is not on main"
    expected=$remote
  fi
  if [ "$remote" != "$expected" ]; then
    echo "mismatch: $tag on $REMOTE points at $remote, expected $expected"
    exit 1
  fi
  echo "ok: $tag on $REMOTE points at $remote"

  local info hash
  info=$(curl -s --max-time 20 -w '\n%{http_code}' "$GOPROXY_URL/$escaped/@v/$version.info" || true)
  if [ "$(tail -n 1 <<<"$info")" != 200 ]; then
    echo "not yet: the module proxy does not serve $path@$version (HTTP $(tail -n 1 <<<"$info"))"
    status=1
  else
    hash=$(sed -nE 's/.*"Hash":"([0-9a-f]+)".*/\1/p' <<<"$info" | head -n 1)
    if [ -n "$hash" ] && [ "$hash" != "$expected" ]; then
      echo "mismatch: the module proxy serves $path@$version from $hash, expected $expected"
      status=1
    else
      echo "ok: the module proxy serves $path@$version"
    fi
  fi

  local code
  code=$(status_of "$SUMDB_URL/lookup/$escaped@$version")
  if [ "$code" = 200 ]; then
    echo "ok: the sum database records $path@$version"
  else
    echo "not yet: the sum database does not record $path@$version (HTTP $code)"
    status=1
  fi

  if [ "$status" -ne 0 ]; then
    echo "run again in a minute; a lookup made before the push can keep a not-found cached for a while"
  fi
  exit "$status"
}

case "$action" in
tag) cmd_tag ;;
verify) cmd_verify ;;
*) usage ;;
esac
