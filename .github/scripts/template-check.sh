#!/usr/bin/env bash
# `mise run template:check`: fails when a path of .template-paths differs between the
# template commit in .template-version and the working tree (ADR 0007). In the template,
# which has no .template-version, there is nothing to check.
#
# The paths are read from .template-paths as it was in that commit, so editing the list in
# the app cannot hide a change (the list is itself one of the paths). The commit is in the
# app's history, so no access to the template repo is needed, only the full history.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

fail() {
  echo "template:check: $*" >&2
  exit 1
}

if [ ! -f .template-version ]; then
  echo "template:check: there is no .template-version, so this is the template itself: nothing to check."
  exit 0
fi

base=$(tr -d '[:space:]' < .template-version)
case "$base" in
  *[!0-9a-f]* | "") fail ".template-version must hold one full commit hash, found '$base'." ;;
esac
[ "${#base}" -eq 40 ] || fail ".template-version must hold a full 40-character commit hash, found '$base'."
git cat-file -e "$base^{commit}" 2>/dev/null ||
  fail "commit $base of .template-version is not in this history (in CI, check out with fetch-depth: 0)."
git cat-file -e "$base:.template-paths" 2>/dev/null ||
  fail "commit $base has no .template-paths."

# One line per path: `dir/` is the whole folder, `!` excludes, globs as in git (`*` does
# not cross `/`, `**` does).
specs=()
while IFS= read -r line || [ -n "$line" ]; do
  line=${line%$'\r'}
  case "$line" in "" | "#"*) continue ;; esac
  magic=glob
  if [ "${line#!}" != "$line" ]; then
    magic=exclude,glob
    line=${line#!}
  fi
  case "$line" in */) line="$line**" ;; esac
  specs+=(":($magic)$line")
done < <(git show "$base:.template-paths")
[ "${#specs[@]}" -gt 0 ] || fail ".template-paths of $base lists no path."

# Tracked files (committed, staged or not) against that commit, plus new untracked ones.
changed=$(
  {
    git -c core.quotePath=false diff --name-status --no-renames "$base" -- "${specs[@]}"
    git -c core.quotePath=false ls-files --others --exclude-standard -- "${specs[@]}" | sed 's/^/?\t/'
  } | sort -k2
)

if [ -n "$changed" ]; then
  {
    echo "template:check: these core files differ from the template at ${base:0:12} (.template-version):"
    while IFS= read -r file; do printf '  %s\n' "$file"; done <<< "$changed"
    echo
    echo "Core paths (.template-paths) are never edited from an app: revert them"
    echo "(git restore --source=$base -- <file>) and make the change in factory-template,"
    echo "where it will arrive with 'mise run template:sync'. If it gets in the way, note it in"
    echo "FACTORY_NOTES.md. See docs/core/runbooks/template-sync.md."
  } >&2
  exit 1
fi
echo "template:check: core matches the template at ${base:0:12}."
