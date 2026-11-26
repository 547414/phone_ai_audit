#!/bin/zsh
# Install Phone AI Audit Mac binaries, launchd daemon, tmux config, and wire Claude/Codex/AGY hooks (with backup).
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
CONFIG_FILE="${CONFIG_FILE:-$ROOT_DIR/config.env}"

if [[ -f "$CONFIG_FILE" ]]; then
  set -a
  source "$CONFIG_FILE"
  set +a
fi

BIN_DIR="$HOME/.local/bin"
MAC_CFG="${MAC_CONFIG_PATH:-$HOME/.config/ai-audit/config.json}"
CFG_DIR="$(dirname "$MAC_CFG")"
BACKUP_DIR="$CFG_DIR/backup-$(date +%Y%m%d-%H%M%S)"
LAUNCHD_LABEL="${MAC_LAUNCHD_LABEL:-com.phone-ai-audit.agent}"
PLIST_PATH="$HOME/Library/LaunchAgents/${LAUNCHD_LABEL}.plist"
LEGACY_CODEX_NOTIFY_BIN="${MAC_CODEX_LEGACY_NOTIFY_BIN:-}"
TMUX_BIN="$(command -v tmux || echo /opt/homebrew/bin/tmux)"

mkdir -p "$BIN_DIR" "$CFG_DIR" "$HOME/.local/run" "$BACKUP_DIR" "$HOME/Library/LaunchAgents"

echo "==> 1. 安装二进制与 ai-tmux 包装脚本到 $BIN_DIR"
install -m 755 "$ROOT_DIR/dist/ai-audit-agent" "$BIN_DIR/ai-audit-agent"
install -m 755 "$ROOT_DIR/dist/ai-audit-hook" "$BIN_DIR/ai-audit-hook"
install -m 755 "$ROOT_DIR/dist/ai-audit" "$BIN_DIR/ai-audit"
if [[ ! -x "$ROOT_DIR/dist/ai-audit-prompt" && -f "$ROOT_DIR/mac-agent/prompt/main.swift" ]] && command -v swiftc >/dev/null 2>&1; then
  echo "    编译 Mac 原生悬浮审批程序 ai-audit-prompt..."
  mkdir -p "$ROOT_DIR/dist"
  swiftc -O "$ROOT_DIR/mac-agent/prompt/main.swift" -o "$ROOT_DIR/dist/ai-audit-prompt"
fi
if [[ -x "$ROOT_DIR/dist/ai-audit-prompt" ]]; then
  install -m 755 "$ROOT_DIR/dist/ai-audit-prompt" "$BIN_DIR/ai-audit-prompt"
fi
install -m 755 "$SCRIPT_DIR/ai-tmux" "$BIN_DIR/ai-tmux"

echo "==> 2. 备份现有配置到 $BACKUP_DIR"
[[ -f "$HOME/.claude/settings.json" ]] && cp -p "$HOME/.claude/settings.json" "$BACKUP_DIR/claude-settings.json.bak"
[[ -f "$HOME/.codex/config.toml" ]] && cp -p "$HOME/.codex/config.toml" "$BACKUP_DIR/codex-config.toml.bak"
[[ -f "$HOME/.codex/hooks.json" ]] && cp -p "$HOME/.codex/hooks.json" "$BACKUP_DIR/codex-hooks.json.bak"
[[ -f "$HOME/.gemini/config/hooks.json" ]] && cp -p "$HOME/.gemini/config/hooks.json" "$BACKUP_DIR/agy-hooks.json.bak"
[[ -f "$HOME/.gemini/antigravity-cli/settings.json" ]] && cp -p "$HOME/.gemini/antigravity-cli/settings.json" "$BACKUP_DIR/agy-settings.json.bak"
[[ -f "$HOME/.local/bin/codex-task-done-notify" ]] && cp -p "$HOME/.local/bin/codex-task-done-notify" "$BACKUP_DIR/codex-task-done-notify.bak"
[[ -f "$HOME/.tmux.conf" ]] && cp -p "$HOME/.tmux.conf" "$BACKUP_DIR/tmux.conf.bak"
if [[ ! -f "$CFG_DIR/last_backup_dir" ]]; then
  echo "$BACKUP_DIR" > "$CFG_DIR/last_backup_dir"
fi

echo "==> 3. 安装 ~/.tmux.conf 并热重载现有 tmux 配置"
cp "$SCRIPT_DIR/tmux.conf" "$HOME/.tmux.conf"
if [[ -x "$TMUX_BIN" ]]; then
  "$TMUX_BIN" source-file "$HOME/.tmux.conf" 2>/dev/null || true
  "$TMUX_BIN" set -g window-size largest 2>/dev/null || true
  "$TMUX_BIN" set -gw window-size largest 2>/dev/null || true
fi

echo "==> 4. 配置 Claude Code / Codex / Antigravity 钩子"
/usr/bin/python3 - <<'PY'
import json
from pathlib import Path

home = Path.home()
hook_bin = str(home / ".local" / "bin" / "ai-audit-hook")

# 4.1 Claude Code (~/.claude/settings.json)
claude_path = home / ".claude" / "settings.json"
claude_path.parent.mkdir(parents=True, exist_ok=True)
claude_cfg = json.loads(claude_path.read_text(encoding="utf-8")) if claude_path.exists() else {}
claude_cfg["hooks"] = {
    "PermissionRequest": [
        {
            "matcher": "",
            "hooks": [
                {
                    "type": "command",
                    "command": f"{hook_bin} claude PermissionRequest",
                    "timeout": 240,
                }
            ],
        }
    ],
    "Notification": [
        {
            "matcher": "",
            "hooks": [
                {
                    "type": "command",
                    "command": f"{hook_bin} claude Notification",
                    "timeout": 15,
                }
            ],
        }
    ],
    "Stop": [
        {
            "matcher": "",
            "hooks": [
                {
                    "type": "command",
                    "command": f"{hook_bin} claude Stop",
                    "timeout": 15,
                }
            ],
        }
    ],
}
claude_path.write_text(json.dumps(claude_cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

# 4.2 Codex (~/.codex/hooks.json)
codex_hooks_path = home / ".codex" / "hooks.json"
codex_hooks_path.parent.mkdir(parents=True, exist_ok=True)
codex_hooks = {
    "hooks": {
        "PermissionRequest": [
            {
                "matcher": ".*",
                "hooks": [
                    {
                        "type": "command",
                        "command": f"{hook_bin} codex PermissionRequest",
                        "timeout": 240,
                    }
                ],
            }
        ],
        "Stop": [
            {
                "matcher": "",
                "hooks": [
                    {
                        "type": "command",
                        "command": f"{hook_bin} codex done",
                        "timeout": 15,
                    }
                ],
            }
        ],
    }
}
codex_hooks_path.write_text(json.dumps(codex_hooks, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

# 4.3 Antigravity (~/.gemini/config/hooks.json)
agy_hooks_path = home / ".gemini" / "config" / "hooks.json"
agy_hooks_path.parent.mkdir(parents=True, exist_ok=True)
agy_hooks = {
    "phone-ai-audit": {
        "Stop": [
            {
                "type": "command",
                "command": f"{hook_bin} agy Stop",
                "timeout": 15,
            }
        ],
        "PreToolUse": [
            {
                "matcher": "ask_question|ask_permission|run_command|write_file|edit_file|write_to_file|replace_file_content|multi_replace_file_content|delete_file|remove_file",
                "hooks": [
                    {
                        "type": "command",
                        "command": f"{hook_bin} agy PreToolUse",
                        "timeout": 240,
                    }
                ],
            }
        ],
    }
}
agy_hooks_path.write_text(json.dumps(agy_hooks, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

# 4.4 Antigravity statusLine (~/.gemini/antigravity-cli/settings.json) for official quota/reset capture
agy_settings_path = home / ".gemini" / "antigravity-cli" / "settings.json"
if agy_settings_path.exists():
    try:
        agy_settings = json.loads(agy_settings_path.read_text(encoding="utf-8"))
        sl = agy_settings.get("statusLine")
        cur_cmd = sl.get("command", "").strip() if isinstance(sl, dict) else ""
        if cur_cmd == "" or "ai-audit-hook" in cur_cmd:
            agy_settings["statusLine"] = {
                "type": "command",
                "command": f"{hook_bin} agy StatusLine",
                "enabled": True,
                "padding": 0,
                "stack_with_default": True,
            }
            agy_settings_path.write_text(json.dumps(agy_settings, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    except Exception:
        pass
PY

# Install codex-task-done-notify wrapper so legacy Codex notify hooks also route to Phone AI Audit
cat > "$BIN_DIR/codex-task-done-notify" <<EOF
#!/bin/zsh
set -u
payload=""
if [[ \$# -gt 0 ]]; then
  payload="\$*"
elif [[ ! -t 0 ]]; then
  payload="\$(cat 2>/dev/null || true)"
fi
LEGACY_BIN=$(printf '%q' "$LEGACY_CODEX_NOTIFY_BIN")
if [[ -n "\$LEGACY_BIN" && -x "\$LEGACY_BIN" ]]; then
  "\$LEGACY_BIN" "turn-ended" "\$@" >/dev/null 2>&1 || true
fi
"\$HOME/.local/bin/ai-audit-hook" codex auto "\$payload" >/dev/null 2>&1 || true
exit 0
EOF
chmod 755 "$BIN_DIR/codex-task-done-notify"

echo "==> 5. 注册并启动 launchd 守护进程 ${LAUNCHD_LABEL}"
cat > "$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LAUNCHD_LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${BIN_DIR}/ai-audit-agent</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${BIN_DIR}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>HOME</key>
    <string>${HOME}</string>
    <key>AI_AUDIT_CONFIG</key>
    <string>${MAC_CFG}</string>
    <key>AI_AUDIT_WEB_ADDR</key>
    <string>${MAC_WEB_ADDR:-}</string>
    <key>AI_AUDIT_PROXY_URL</key>
    <string>${MAC_PROXY_URL:-${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}}</string>
    <key>HTTP_PROXY</key>
    <string>${HTTP_PROXY:-${http_proxy:-${MAC_PROXY_URL:-}}}</string>
    <key>HTTPS_PROXY</key>
    <string>${HTTPS_PROXY:-${https_proxy:-${MAC_PROXY_URL:-}}}</string>
    <key>NO_PROXY</key>
    <string>${NO_PROXY:-${no_proxy:-localhost,127.0.0.1}}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${CFG_DIR}/agent.stdout.log</string>
  <key>StandardErrorPath</key>
  <string>${CFG_DIR}/agent.stderr.log</string>
</dict>
</plist>
EOF

launchctl bootout "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
launchctl kickstart -k "gui/$(id -u)/${LAUNCHD_LABEL}"

echo "==> 完成！提示："
echo "  1. 请确认配置文件已填写: ${MAC_CFG}（可参考 mac-agent/config.example.json）"
echo "  2. 首次使用 Codex Hook，请打开 codex 输入 /hooks 信任 ~/.codex/hooks.json"
echo "  3. 查看状态：ai-audit status"
echo "  4. 查看 Agent 用量 / 打开网页看板：ai-audit usage / ai-audit web (${MAC_WEB_ADDR:-127.0.0.1:9878})"
echo "  5. 生成手机扫码配置：ai-audit pair"
echo "  6. 启动可被手机接管的终端：ai-tmux claude / ai-tmux codex / ai-tmux agy"
