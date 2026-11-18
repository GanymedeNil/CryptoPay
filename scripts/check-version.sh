#!/usr/bin/env bash
# Checks that the release version is consistent everywhere it is written down:
#
#   VERSION  ==  web/package.json "version"  ==  a "## [VERSION]" heading in CHANGELOG.md
#
# CHANGELOG.md must also have an "## [Unreleased]" section, and VERSION must be
# a semantic version (X.Y.Z or X.Y.Z-prerelease, no leading "v").
#
#   scripts/check-version.sh                  # CI and `make check-version`
#   scripts/check-version.sh --tag v1.2.3     # release workflow: the git tag must be v$VERSION
set -euo pipefail
cd "$(dirname "$0")/.."

tag=""
if [ "${1:-}" = "--tag" ]; then
  tag="${2:?usage: check-version.sh [--tag vX.Y.Z]}"
elif [ $# -gt 0 ]; then
  echo "usage: check-version.sh [--tag vX.Y.Z]" >&2
  exit 2
fi

errors=0
fail() { echo "check-version: $1" >&2; errors=$((errors + 1)); }

[ -f VERSION ] || { echo "check-version: VERSION is missing" >&2; exit 1; }
version="$(tr -d '[:space:]' < VERSION)"

semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'
[[ "$version" =~ $semver ]] || fail "VERSION '$version' is not a semantic version (X.Y.Z[-prerelease], no leading v)"

web_version="$(grep -m1 '^  "version":' web/package.json | sed -E 's/.*"version": *"([^"]*)".*/\1/')"
[ "$web_version" = "$version" ] || fail "web/package.json version '$web_version' differs from VERSION '$version'"

# Exact heading match (the dots in the version are not regex wildcards here).
has_heading() { awk -v want="[$1]" '$1 == "##" && $2 == want { found = 1 } END { exit !found }' CHANGELOG.md; }
has_heading Unreleased || fail "CHANGELOG.md has no '## [Unreleased]' section"
has_heading "$version" || fail "CHANGELOG.md has no '## [$version]' section"

if [ -n "$tag" ] && [ "$tag" != "v$version" ]; then
  fail "git tag '$tag' does not match VERSION (expected 'v$version')"
fi

if [ "$errors" -ne 0 ]; then
  exit 1
fi
echo "version $version is consistent${tag:+ with tag $tag}"
