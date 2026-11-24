#!/usr/bin/env bash
# `mise run template:init`: turns a fresh clone of factory-template into a new app. The
# clone's origin (the template) becomes the `template` remote and .template-version records
# the template commit the app starts from (ADR 0007, docs/core/runbooks/template-sync.md).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

fail() {
  echo "template:init: $*" >&2
  exit 1
}

[ ! -e .template-version ] || fail ".template-version already exists: this app is initialised (use 'mise run template:sync')."
! git remote get-url template >/dev/null 2>&1 || fail "the 'template' remote already exists."
template_url=$(git remote get-url origin 2>/dev/null) || fail "there is no 'origin' remote: run this in a fresh clone of factory-template."
[ -z "$(git status --porcelain)" ] || fail "the working tree is not clean."

git remote rename origin template
commit=$(git rev-parse HEAD)
echo "$commit" > .template-version

cat <<MSG
template:init: 'origin' is now 'template' ($template_url).
.template-version = $commit

Next steps (create the empty app repo first, e.g. 'gh repo create <owner>/<app> --private'):
  git remote add origin <url of the app repo>
  git push -u origin main                    # the template history, as is
  git switch -c chore/template-init
  git add .template-version
  git commit -m "chore: inicia la app desde factory-template"
  git push -u origin chore/template-init     # and open the PR
MSG
