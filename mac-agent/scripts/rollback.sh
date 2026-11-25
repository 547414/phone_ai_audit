#!/bin/zsh
# Roll back Mac hooks and stop ai-audit-agent cleanly.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
CONFIG_FILE="${CONFIG_FILE:-$ROOT_DIR/config.env}"

if [[ -f "$CONFIG_FILE" ]]; then
  set -a
  source "$CONFIG_FILE"
  set +a
fi

MAC_CFG="${MAC_CONFIG_PATH:-$HOME/.config/ai-audit/config.json}"
CFG_DIR="$(dirname "$MAC_CFG")"
LAUNCHD_LABEL="${MAC_LAUNCHD_LABEL:-com.phone-ai-audit.agent}"
PLIST_PATH="$HOME/Library/LaunchAgents/${LAUNCHD_LABEL}.plist"

launchctl bootout "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null || true
rm -f "$PLIST_PATH"

if [[ -f "$CFG_DIR/last_backup_dir" ]]; then
  BACKUP_DIR="$(cat "$CFG_DIR/last_backup_dir")"
  echo "从备份恢复: $BACKUP_DIR"
  [[ -f "$BACKUP_DIR/claude-settings.json.bak" ]] && cp -p "$BACKUP_DIR/claude-settings.json.bak" "$HOME/.claude/settings.json"
  [[ -f "$BACKUP_DIR/codex-config.toml.bak" ]] && cp -p "$BACKUP_DIR/codex-config.toml.bak" "$HOME/.codex/config.toml"
  [[ -f "$BACKUP_DIR/agy-hooks.json.bak" ]] && cp -p "$BACKUP_DIR/agy-hooks.json.bak" "$HOME/.gemini/config/hooks.json"
  [[ -f "$BACKUP_DIR/codex-task-done-notify.bak" ]] && cp -p "$BACKUP_DIR/codex-task-done-notify.bak" "$HOME/.local/bin/codex-task-done-notify"
  if [[ -f "$BACKUP_DIR/codex-hooks.json.bak" ]]; then
    cp -p "$BACKUP_DIR/codex-hooks.json.bak" "$HOME/.codex/hooks.json"
  else
    rm -f "$HOME/.codex/hooks.json"
  fi
  echo "已回滚完成。"
else
  echo "未找到备份目录记录。"
fi
