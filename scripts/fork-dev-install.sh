#!/usr/bin/env bash
# Build the fork's desktop app and replace the installed /Applications copy.
# See FORK.md for the full workflow. Usage:
#   scripts/fork-dev-install.sh              # build + replace installed app
#   scripts/fork-dev-install.sh --no-install # build only, leave it in frontend/out
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FORK_REPO="YingkeSu/agent-orchestrator"
APP_NAME="Agent Orchestrator"
INSTALL_DIR="/Applications"

# AO_RELEASE_REPO bakes the electron-updater feed (app-update.yml) into the
# bundle at package time. Pointing it at the fork means the finished app can
# only ever look for updates on the fork (which publishes none), so an official
# release can never overwrite this local build.
export AO_RELEASE_REPO="${AO_RELEASE_REPO:-$FORK_REPO}"

cd "$REPO_ROOT/frontend"
if [ ! -d node_modules ]; then
	echo "==> frontend/node_modules missing; running npm install"
	npm install --no-fund --no-audit
fi

echo "==> Packaging (Go daemon + tmux + browser runtime + ACP runtime + Electron)"
npm run package

# electron-forge wipes its per-platform out/ dir each run; still pick the
# newest .app defensively in case of stale artifacts.
APP_PATH="$(find out -maxdepth 2 -type d -name "*.app" -print0 | xargs -0 stat -f '%m %N' | sort -rn | head -1 | cut -d' ' -f2-)"
if [ -z "$APP_PATH" ] || [ ! -d "$APP_PATH" ]; then
	echo "error: no .app found under frontend/out" >&2
	exit 1
fi
echo "==> Built: $APP_PATH"

if [ "${1:-}" = "--no-install" ]; then
	echo "==> --no-install given; built app left in place"
	exit 0
fi

if pgrep -x "agent-orchestrator" >/dev/null 2>&1; then
	echo "==> Quitting running $APP_NAME"
	osascript -e "quit app \"$APP_NAME\"" >/dev/null 2>&1 || true
	for _ in $(seq 1 15); do
		pgrep -x "agent-orchestrator" >/dev/null 2>&1 || break
		sleep 1
	done
	if pgrep -x "agent-orchestrator" >/dev/null 2>&1; then
		echo "error: $APP_NAME still running after 15s; close it and rerun" >&2
		exit 1
	fi
fi

echo "==> Replacing $INSTALL_DIR/$APP_NAME.app"
rm -rf "$INSTALL_DIR/$APP_NAME.app"
cp -R "$APP_PATH" "$INSTALL_DIR/"
echo "==> Done. Unsigned build: if macOS blocks first launch, allow it under"
echo "    System Settings > Privacy & Security."
