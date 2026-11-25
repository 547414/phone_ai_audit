#!/bin/zsh
# Deploy ai-audit-relay to your remote Linux server and configure Nginx + systemd.
# Reads settings from ./config.env (copy from config.env.example).
# Preserves existing tokens if already configured in MAC_CONFIG_PATH.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
CONFIG_FILE="${CONFIG_FILE:-$ROOT_DIR/config.env}"

if [[ ! -f "$CONFIG_FILE" ]]; then
  echo "Missing $CONFIG_FILE. Copy config.env.example to config.env and edit your settings:" >&2
  echo "  cp \"$ROOT_DIR/config.env.example\" \"$ROOT_DIR/config.env\"" >&2
  exit 1
fi

set -a
source "$CONFIG_FILE"
set +a

RELAY_DOMAIN="${RELAY_DOMAIN:?RELAY_DOMAIN must be set in config.env}"
RELAY_PUBLIC_PORT="${RELAY_PUBLIC_PORT:-9876}"
RELAY_PUBLIC_URL="${RELAY_PUBLIC_URL:-https://${RELAY_DOMAIN}:${RELAY_PUBLIC_PORT}}"
RELAY_LISTEN_ADDR="${RELAY_LISTEN_ADDR:-127.0.0.1:8787}"
RELAY_SSH_HOST="${RELAY_SSH_HOST:?RELAY_SSH_HOST must be set in config.env}"
RELAY_REMOTE_USER="${RELAY_REMOTE_USER:-ubuntu}"
RELAY_REMOTE_GROUP="${RELAY_REMOTE_GROUP:-$RELAY_REMOTE_USER}"
RELAY_REMOTE_DIR="${RELAY_REMOTE_DIR:-/home/${RELAY_REMOTE_USER}/ai-audit-relay}"
RELAY_SERVICE_NAME="${RELAY_SERVICE_NAME:-ai-audit-relay}"
RELAY_NGINX_SITE_NAME="${RELAY_NGINX_SITE_NAME:-ai-audit}"
RELAY_SSL_CERT="${RELAY_SSL_CERT:?RELAY_SSL_CERT must be set in config.env}"
RELAY_SSL_KEY="${RELAY_SSL_KEY:?RELAY_SSL_KEY must be set in config.env}"
RELAY_SSL_OPTIONS="${RELAY_SSL_OPTIONS:-}"
RELAY_SSL_DHPARAM="${RELAY_SSL_DHPARAM:-}"
RELAY_SUDO_NOPASSWD="${RELAY_SUDO_NOPASSWD:-true}"
MAC_CFG="${MAC_CONFIG_PATH:-$HOME/.config/ai-audit/config.json}"
MAC_FILE_ROOTS="${MAC_FILE_ROOTS:-$HOME/workspace}"

BIN_LOCAL="$ROOT_DIR/dist/ai-audit-relay-linux-amd64"
SERVICE_TEMPLATE="$SCRIPT_DIR/ai-audit-relay.service.example"
NGINX_TEMPLATE="$SCRIPT_DIR/nginx-audit.conf.example"
SERVICE_OVERRIDE="$SCRIPT_DIR/ai-audit-relay.service"
NGINX_OVERRIDE="$SCRIPT_DIR/nginx-audit.conf"

if [[ ! -x "$BIN_LOCAL" ]]; then
  echo "Missing $BIN_LOCAL, build relay first:" >&2
  echo "  (cd relay && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../dist/ai-audit-relay-linux-amd64 ./cmd/relay)" >&2
  exit 1
fi

SSH_PASS="${RELAY_SSH_PASS:-}"
if [[ -z "$SSH_PASS" && -n "${RELAY_SSH_PASS_SCRIPT:-}" && -f "$RELAY_SSH_PASS_SCRIPT" ]]; then
  SSH_PASS="$(sed -n 's/.*sshpass -p "\([^"]*\)".*/\1/p' "$RELAY_SSH_PASS_SCRIPT" | head -n 1)"
fi

SSH_OPTS=(-o ClearAllForwardings=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)

ssh_run() {
  if [[ -n "$SSH_PASS" ]]; then
    SSHPASS="$SSH_PASS" sshpass -e ssh "${SSH_OPTS[@]}" "$RELAY_SSH_HOST" "$@"
  else
    ssh "${SSH_OPTS[@]}" "$RELAY_SSH_HOST" "$@"
  fi
}

scp_put() {
  if [[ -n "$SSH_PASS" ]]; then
    SSHPASS="$SSH_PASS" sshpass -e scp "${SSH_OPTS[@]}" "$1" "${RELAY_SSH_HOST}:$2"
  else
    scp "${SSH_OPTS[@]}" "$1" "${RELAY_SSH_HOST}:$2"
  fi
}

remote_sudo_prefix() {
  if [[ "$RELAY_SUDO_NOPASSWD" == "true" || -z "$SSH_PASS" ]]; then
    printf '%s' "sudo"
  else
    printf 'echo %q | sudo -S -p ""' "$SSH_PASS"
  fi
}

# Reuse existing tokens if present locally, otherwise generate cryptographically strong tokens.
MAC_TOKEN=""
APP_TOKEN=""
if [[ -f "$MAC_CFG" ]]; then
  MAC_TOKEN="$(/usr/bin/python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("mac_token",""))' "$MAC_CFG" 2>/dev/null || true)"
  APP_TOKEN="$(/usr/bin/python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("app_token",""))' "$MAC_CFG" 2>/dev/null || true)"
fi
if [[ -z "$MAC_TOKEN" ]]; then
  MAC_TOKEN="mac_$(openssl rand -hex 24)"
fi
if [[ -z "$APP_TOKEN" ]]; then
  APP_TOKEN="app_$(openssl rand -hex 24)"
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

SERVICE_RENDERED="$TMP_DIR/${RELAY_SERVICE_NAME}.service"
NGINX_RENDERED="$TMP_DIR/${RELAY_NGINX_SITE_NAME}.conf"
TMP_ENV="$TMP_DIR/.env"

if [[ -f "$SERVICE_OVERRIDE" ]]; then
  cp "$SERVICE_OVERRIDE" "$SERVICE_RENDERED"
else
  /usr/bin/python3 - "$SERVICE_TEMPLATE" "$SERVICE_RENDERED" <<'PY'
import os, sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, "r", encoding="utf-8").read()
for k in ("RELAY_REMOTE_USER", "RELAY_REMOTE_GROUP", "RELAY_REMOTE_DIR"):
    text = text.replace("{{" + k + "}}", os.environ.get(k, ""))
open(dst, "w", encoding="utf-8").write(text)
PY
fi

if [[ -f "$NGINX_OVERRIDE" ]]; then
  cp "$NGINX_OVERRIDE" "$NGINX_RENDERED"
else
  /usr/bin/python3 - "$NGINX_TEMPLATE" "$NGINX_RENDERED" <<'PY'
import os, sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, "r", encoding="utf-8").read()
extra_lines = []
if os.environ.get("RELAY_SSL_OPTIONS"):
    extra_lines.append(f"    include {os.environ['RELAY_SSL_OPTIONS']};")
if os.environ.get("RELAY_SSL_DHPARAM"):
    extra_lines.append(f"    ssl_dhparam {os.environ['RELAY_SSL_DHPARAM']};")
extra = "\n".join(extra_lines)
replacements = {
    "RELAY_PUBLIC_PORT": os.environ.get("RELAY_PUBLIC_PORT", "9876"),
    "RELAY_DOMAIN": os.environ.get("RELAY_DOMAIN", ""),
    "RELAY_SSL_CERT": os.environ.get("RELAY_SSL_CERT", ""),
    "RELAY_SSL_KEY": os.environ.get("RELAY_SSL_KEY", ""),
    "RELAY_LISTEN_ADDR": os.environ.get("RELAY_LISTEN_ADDR", "127.0.0.1:8787"),
}
for k, v in replacements.items():
    text = text.replace("{{" + k + "}}", v)
if extra:
    text = text.replace("    {{RELAY_SSL_EXTRA}}", extra)
else:
    text = text.replace("    {{RELAY_SSL_EXTRA}}\n", "")
open(dst, "w", encoding="utf-8").write(text)
PY
fi

cat > "$TMP_ENV" <<EOF
LISTEN_ADDR=${RELAY_LISTEN_ADDR}
DB_PATH=${RELAY_REMOTE_DIR}/data/audit.db
MAC_TOKEN=${MAC_TOKEN}
APP_TOKEN=${APP_TOKEN}
PUBLIC_BASE_URL=${RELAY_PUBLIC_URL}
EOF

echo "==> 1. 准备远端目录 ${RELAY_SSH_HOST}:${RELAY_REMOTE_DIR}"
ssh_run "mkdir -p '${RELAY_REMOTE_DIR}/data' && chmod 700 '${RELAY_REMOTE_DIR}'"

echo "==> 2. 上传二进制、systemd 与 Nginx 配置"
scp_put "$BIN_LOCAL" "${RELAY_REMOTE_DIR}/ai-audit-relay.new"
scp_put "$SERVICE_RENDERED" "${RELAY_REMOTE_DIR}/${RELAY_SERVICE_NAME}.service"
scp_put "$NGINX_RENDERED" "${RELAY_REMOTE_DIR}/${RELAY_NGINX_SITE_NAME}.conf"
scp_put "$TMP_ENV" "${RELAY_REMOTE_DIR}/.env"

echo "==> 3. 启动 systemd 服务与 Nginx (${RELAY_PUBLIC_PORT})"
ssh_run "chmod 600 '${RELAY_REMOTE_DIR}/.env' && chmod 755 '${RELAY_REMOTE_DIR}/ai-audit-relay.new' && mv -f '${RELAY_REMOTE_DIR}/ai-audit-relay.new' '${RELAY_REMOTE_DIR}/ai-audit-relay'"

SUDO="$(remote_sudo_prefix)"
ssh_run "${SUDO} cp '${RELAY_REMOTE_DIR}/${RELAY_SERVICE_NAME}.service' '/etc/systemd/system/${RELAY_SERVICE_NAME}.service' && \
  ${SUDO} cp '${RELAY_REMOTE_DIR}/${RELAY_NGINX_SITE_NAME}.conf' '/etc/nginx/sites-available/${RELAY_NGINX_SITE_NAME}' && \
  ${SUDO} ln -sf '/etc/nginx/sites-available/${RELAY_NGINX_SITE_NAME}' '/etc/nginx/sites-enabled/${RELAY_NGINX_SITE_NAME}' && \
  ${SUDO} systemctl daemon-reload && \
  ${SUDO} systemctl enable --now '${RELAY_SERVICE_NAME}.service' && \
  ${SUDO} systemctl restart '${RELAY_SERVICE_NAME}.service' && \
  ${SUDO} nginx -t && \
  ${SUDO} systemctl reload nginx"

echo "==> 4. 写入 Mac 本机配置 $MAC_CFG"
mkdir -p "$(dirname "$MAC_CFG")"
/usr/bin/python3 - "$MAC_CFG" "$RELAY_PUBLIC_URL" "$MAC_TOKEN" "$APP_TOKEN" "$MAC_FILE_ROOTS" <<'PY'
import json, os, socket, sys
path, server_url, mac_tok, app_tok, roots_raw = sys.argv[1:6]
cfg = {}
if os.path.exists(path):
    try:
        cfg = json.load(open(path, "r", encoding="utf-8"))
    except Exception:
        cfg = {}
roots = [os.path.expanduser(r.strip()) for r in roots_raw.split(",") if r.strip()]
cfg.update({
    "server_url": server_url.rstrip("/"),
    "mac_token": mac_tok,
    "app_token": app_tok,
    "socket_path": cfg.get("socket_path") or os.path.expanduser("~/.local/run/ai-audit.sock"),
    "hostname": cfg.get("hostname") or socket.gethostname(),
})
if roots:
    cfg.setdefault("file_roots", roots)
with open(path, "w", encoding="utf-8") as f:
    json.dump(cfg, f, indent=2, ensure_ascii=False)
    f.write("\n")
os.chmod(path, 0o600)
PY

echo "==> 5. 健康检查（服务器本机回环 & 服务状态）"
ssh_run "sleep 1 && curl -fsS 'http://${RELAY_LISTEN_ADDR}/api/v1/health' && echo '' && systemctl status '${RELAY_SERVICE_NAME}.service' --no-pager | head -n 15"

echo "==> 6. 从本机测试公网 ${RELAY_PUBLIC_URL}/api/v1/health"
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
if curl --noproxy '*' -fsS --connect-timeout 5 "${RELAY_PUBLIC_URL}/api/v1/health"; then
  echo ""
  echo "公网 ${RELAY_PUBLIC_URL} 已联通！"
else
  echo ""
  echo "提示：远端服务与 Nginx 已就绪，但公网 ${RELAY_PUBLIC_URL} 暂未通，请检查安全组、防火墙与 TLS 证书。"
fi
