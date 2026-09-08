#!/usr/bin/env bash
# Sync the fork's main from upstream, then rebase the mods branch onto it.
# See FORK.md for the full workflow. Push of the rebased mods branch is left
# manual on purpose (--force-with-lease rewrites the remote branch).
set -euo pipefail

cd "$(dirname "$0")/.."
MODS_BRANCH="${MODS_BRANCH:-mods/main}"

git fetch upstream

git checkout main
git merge --ff-only upstream/main
git push origin main
echo "==> main synced with upstream and pushed to origin"

if git show-ref --verify --quiet "refs/heads/$MODS_BRANCH"; then
	git checkout "$MODS_BRANCH"
	git rebase main
	echo "==> $MODS_BRANCH rebased onto main."
	echo "    Verify (cd backend && go test ./... ; cd frontend && npm run typecheck),"
	echo "    then push with: git push origin $MODS_BRANCH --force-with-lease"
else
	echo "==> branch $MODS_BRANCH not found; create it with: git checkout -b $MODS_BRANCH main"
fi
