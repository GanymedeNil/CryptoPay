#!/usr/bin/env bash
# Prints the CHANGELOG.md section of one version (without its heading): the
# body of the GitHub release. Fails when the section is missing or empty.
#
#   scripts/release-notes.sh 0.1.0
set -euo pipefail
cd "$(dirname "$0")/.."

version="${1:?usage: release-notes.sh VERSION}"
notes="$(awk -v want="[$version]" '
  $1 == "##" { if (in_section) exit; in_section = ($2 == want); next }
  in_section { print }
' CHANGELOG.md | awk 'NF { started = 1 } started' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')"

if [ -z "$notes" ]; then
  echo "release-notes: CHANGELOG.md has no entry for $version" >&2
  exit 1
fi
printf '%s\n' "$notes"
