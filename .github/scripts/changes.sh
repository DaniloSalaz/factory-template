#!/usr/bin/env bash
# Decides whether a change touches code (code=true) or only documentation (code=false), so
# CI can skip the heavy jobs. Anything it cannot prove is documentation counts as code.
# See docs/platform/runbooks/ci.md.
#
# Usage: changes.sh <pull_request|push> <base> <head>
# Writes `code=true|false` to $GITHUB_OUTPUT when set, and always to stdout.
set -uo pipefail

event=${1:-}
base=${2:-}
head=${3:-}

emit() {
  echo "code=$1"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    echo "code=$1" >> "$GITHUB_OUTPUT"
  fi
  exit 0
}

code() {
  echo "Counts as code: $1" >&2
  emit true
}

case "$event" in
  pull_request) range="$base...$head" ;; # what the PR adds since the merge base
  push) range="$base..$head" ;;
  *) code "unknown event '$event'" ;;
esac

if [ -z "$base" ] || [ -z "$head" ]; then
  code "missing base or head"
fi
# A new branch, or the first push, has no previous commit to compare with.
if [[ "$base" =~ ^0+$ ]]; then
  code "no previous commit ($base)"
fi

echo "Range: $range" >&2

files=$(mktemp)
trap 'rm -f "$files"' EXIT
# --no-renames lists both sides of a rename, so moving a code file into docs/ is code.
if ! git diff --name-only --no-renames -z "$range" -- > "$files"; then
  code "git diff $range failed"
fi

count=0
while IFS= read -r -d '' path; do
  count=$((count + 1))
  case "$path" in
    # Code folders win over *.md: a README inside them may be embedded, generated or tested.
    .github/* | api/* | web/* | infra/* | api-spec/*) code "$path" ;;
    docs/* | *.md | LICENSE) ;;
    *) code "$path" ;;
  esac
done < "$files"

if [ "$count" -eq 0 ]; then
  code "empty diff"
fi

echo "Only documentation: $count file(s)" >&2
emit false
