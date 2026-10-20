#!/usr/bin/env bash
# Check that every action used by a workflow is pinned to a full commit SHA.
#
# A tag is a mutable name: whoever can move it in the action's repository
# decides what this repository's CI runs, including the release job, which
# holds a token that can write to the repository. A commit SHA names one
# immutable tree, so the only way to change what runs is to change a
# workflow file — a diff, reviewed like any other.
#
# Dependabot still updates pinned actions: it rewrites the SHA and the
# version comment beside it, so pinning does not cost us the updates.
#
# Local actions (./path) and container images (docker://image) are not
# fetched from GitHub and are left alone.
#
# Usage: bash .github/scripts/check-pinned-actions.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

shopt -s nullglob
workflows=(.github/workflows/*.yml .github/workflows/*.yaml)
if [ ${#workflows[@]} -eq 0 ]; then
  echo "no workflow files found under .github/workflows" >&2
  exit 1
fi

# Print "file:line:reference" for every "uses:" in the workflows. The
# reference is the action and what it is pinned to, with any trailing
# comment and quoting removed.
references() {
  awk '
    /^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*/ {
      ref = $0
      sub(/^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*/, "", ref)
      sub(/[[:space:]]*#.*$/, "", ref)
      gsub(/[ \t\r]/, "", ref)
      gsub(/"/, "", ref)
      gsub(/\047/, "", ref)
      print FILENAME ":" FNR ":" ref
    }
  ' "${workflows[@]}"
}

status=0
while IFS= read -r reference; do
  file="${reference%%:*}"
  rest="${reference#*:}"
  line="${rest%%:*}"
  action="${rest#*:}"

  case "$action" in
    ./*|docker://*) continue ;;
  esac

  sha="${action##*@}"
  if [[ ! "$sha" =~ ^[0-9a-f]{40}$ ]]; then
    printf '%s:%s: %s is not pinned to a full commit SHA\n' "$file" "$line" "$action" >&2
    status=1
  fi
done < <(references)

if [ "$status" -ne 0 ]; then
  echo "pin every action to the commit of a tagged release, with the version in a trailing comment" >&2
  exit 1
fi

echo "every action is pinned to a full commit SHA"
