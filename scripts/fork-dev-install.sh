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
	npm ci --no-fund --no-audit
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
# Repackaging invalidates Electron's upstream seal. Local builds need a fresh
# ad-hoc seal; retain the runtime entitlements on the nested executables.
if [ -z "${APPLE_SIGNING_IDENTITY:-}${CSC_LINK:-}" ]; then
    codesign --force --deep --sign - --preserve-metadata=entitlements "$APP_PATH"
fi

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

# Stage the complete bundle before touching the installed copy. Keep the old
# version under AO state so a failed launch can be rolled back without download.
STAGE_DIR="$(mktemp -d "$INSTALL_DIR/.ao-install.XXXXXX")"
BACKUP_DIR="$HOME/.ao/backups/desktop-$(date +%Y%m%d-%H%M%S)-$$"
trap 'rmdir "$STAGE_DIR" 2>/dev/null || true' EXIT
ditto "$APP_PATH" "$STAGE_DIR/$APP_NAME.app"
if [ -d "$INSTALL_DIR/$APP_NAME.app" ]; then
    mkdir -p "$BACKUP_DIR"
    mv "$INSTALL_DIR/$APP_NAME.app" "$BACKUP_DIR/$APP_NAME.app"
    echo "==> Previous app saved to $BACKUP_DIR"
fi
if ! mv "$STAGE_DIR/$APP_NAME.app" "$INSTALL_DIR/$APP_NAME.app"; then
    if [ -d "$BACKUP_DIR/$APP_NAME.app" ]; then
        mv "$BACKUP_DIR/$APP_NAME.app" "$INSTALL_DIR/$APP_NAME.app"
    fi
    exit 1
fi
echo "==> Done. Unsigned build: if macOS blocks first launch, allow it under"
echo "    System Settings > Privacy & Security."
