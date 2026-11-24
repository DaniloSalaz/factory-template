#!/usr/bin/env bash
# `mise run template:sync`: merges template/main into the current branch, records the new
# template commit in .template-version and regenerates the code (ADR 0007). Conflicts are
# left for a person to resolve: no merge strategy (-X ours/theirs) is forced, because it
# would silently pick a side in shared files. See docs/core/runbooks/template-sync.md.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

fail() {
  echo "template:sync: $*" >&2
  exit 1
}

branch=$(git symbolic-ref --short -q HEAD) || fail "HEAD is detached: switch to a branch."
[ "$branch" != "main" ] || fail "you are on main: git switch -c chore/template-sync"
[ -f .template-version ] || fail "there is no .template-version: this is the template, or the app is not initialised (mise run template:init)."
git remote get-url template >/dev/null 2>&1 || fail "there is no 'template' remote (git remote add template <url of factory-template>)."
[ -z "$(git status --porcelain)" ] || fail "the working tree is not clean."

git fetch template
old=$(tr -d '[:space:]' < .template-version)
new=$(git rev-parse "refs/remotes/template/main^{commit}")

if git merge-base --is-ancestor "$new" HEAD; then
  echo "template:sync: already up to date with template/main (${new:0:12})."
  exit 0
fi

echo "template:sync: merging template/main ${old:0:12}..${new:0:12}:"
git --no-pager log --oneline --no-merges "$old..$new"
echo

message="chore: sincroniza con la plantilla (${new:0:12})"
conflict=0
git merge --no-ff --no-commit -m "$message" refs/remotes/template/main || conflict=1
echo "$new" > .template-version
git add .template-version

if [ "$conflict" -eq 1 ]; then
  cat >&2 <<MSG

template:sync: the merge has conflicts (git status lists them). .template-version is
already updated in the merge. To finish:
  1. Resolve each file by hand. Core paths (.template-paths) take the template's version:
       git checkout --theirs -- <file>
     Shared files ([C]: mise.toml, README.md, compose.yaml, go.mod…) keep both changes.
  2. git add <files>
  3. mise run generate && git add api/gen web/src/gen api-spec/openapi.gen.yaml
  4. git commit --no-edit
  5. mise run template:check
To give up: git merge --abort
MSG
  exit 1
fi

mise run generate || fail "the merge is clean but 'mise run generate' failed; the merge is still in progress.
Fix it, then: git add api/gen web/src/gen api-spec/openapi.gen.yaml && git commit --no-edit
To give up: git merge --abort"
git add -- api/gen web/src/gen api-spec/openapi.gen.yaml
git commit --no-edit
mise run template:check
echo "template:sync: merged template/main ${new:0:12}. Review the CHANGELOG.md 'Cómo migrar' notes, run 'mise run ci' and open the PR."
