#!/usr/bin/env bash
# Install tickets_please as a systemd --user service bound to 127.0.0.1:8765.
# Idempotent. Pass --uninstall to remove.

set -euo pipefail

cyan='\033[0;36m'; yellow='\033[1;33m'; green='\033[0;32m'; red='\033[0;31m'; gray='\033[0;37m'; nc='\033[0m'

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DST="${HOME}/.local/bin/tickets_please"
UNIT_DST="${HOME}/.config/systemd/user/tickets_please.service"
UNIT_SRC="${REPO_DIR}/scripts/tickets_please.service.tmpl"
HEALTH_URL="http://127.0.0.1:8765/healthz"

usage() {
    cat <<EOF
Usage: $0 [--uninstall]

Installs tickets_please as a systemd user service that runs the HTTP MCP
server on 127.0.0.1:8765. The data dir for each project is wherever you
register it via register_agent {project_path: ...}.

After install, wire Claude Code:
  claude mcp add --transport http tickets_please ${HEALTH_URL%/healthz}/mcp
EOF
}

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || { echo -e "${red}missing required command: $1${nc}"; exit 1; }
}

uninstall() {
    echo -e "${cyan}=== tickets_please uninstall ===${nc}"
    if systemctl --user is-enabled tickets_please.service >/dev/null 2>&1 \
        || systemctl --user is-active tickets_please.service >/dev/null 2>&1; then
        echo -e "${yellow}Stopping + disabling service...${nc}"
        systemctl --user disable --now tickets_please.service || true
    fi
    if [[ -f "$UNIT_DST" ]]; then
        rm -f "$UNIT_DST"
        echo -e "${gray}Removed $UNIT_DST${nc}"
    fi
    systemctl --user daemon-reload || true
    if [[ -f "$BIN_DST" ]]; then
        rm -f "$BIN_DST"
        echo -e "${gray}Removed $BIN_DST${nc}"
    fi
    echo -e "${green}Uninstall complete.${nc}"
    echo -e "${gray}Data left in place: ~/.tickets_please/ (delete manually if you want a clean slate).${nc}"
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
    --uninstall) uninstall; exit 0 ;;
    "") : ;;
    *) echo -e "${red}unknown arg: $1${nc}"; usage; exit 1 ;;
esac

echo -e "${cyan}=== tickets_please install ===${nc}"

require_cmd go
require_cmd make
require_cmd systemctl
require_cmd loginctl
require_cmd envsubst
require_cmd curl

echo -e "\n${yellow}[1/6] Building binary...${nc}"
make -C "$REPO_DIR" build

echo -e "\n${yellow}[2/6] Installing binary to ${BIN_DST}...${nc}"
mkdir -p "$(dirname "$BIN_DST")"
install -m 0755 "${REPO_DIR}/tickets_please" "$BIN_DST"
case ":${PATH}:" in
    *":${HOME}/.local/bin:"*) ;;
    *) echo -e "${yellow}note: ${HOME}/.local/bin is not on PATH — add it to your shell rc if you want to run 'tickets_please' directly${nc}" ;;
esac

echo -e "\n${yellow}[3/6] Initialising config + data dirs...${nc}"
make -C "$REPO_DIR" init-config init-data >/dev/null

echo -e "\n${yellow}[4/6] Installing systemd --user unit...${nc}"
mkdir -p "$(dirname "$UNIT_DST")"
envsubst < "$UNIT_SRC" > "$UNIT_DST"
systemctl --user daemon-reload

echo -e "\n${yellow}[5/6] Enabling lingering so the service runs without an active login...${nc}"
if [[ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || true)" != "yes" ]]; then
    echo -e "${gray}Running: sudo loginctl enable-linger $USER${nc}"
    sudo loginctl enable-linger "$USER"
else
    echo -e "${gray}Linger already enabled.${nc}"
fi

echo -e "\n${yellow}[6/6] Starting service...${nc}"
systemctl --user enable --now tickets_please.service

printf "${gray}Waiting for %s${nc}" "$HEALTH_URL"
ok=false
for _ in $(seq 1 15); do
    if curl -fsS --max-time 1 "$HEALTH_URL" >/dev/null 2>&1; then
        ok=true; break
    fi
    printf '.'
    sleep 1
done
printf '\n'

if [[ "$ok" != true ]]; then
    echo -e "${red}Service did not respond within 15s. Recent logs:${nc}"
    journalctl --user -u tickets_please.service -n 30 --no-pager || true
    exit 1
fi

echo -e "\n${green}=== Install complete ===${nc}"
echo -e "${cyan}Service:${nc}  systemctl --user status tickets_please"
echo -e "${cyan}Logs:${nc}     journalctl --user -u tickets_please -f"
echo -e "${cyan}Web UI:${nc}   ${HEALTH_URL%/healthz}/"
echo -e "${cyan}Wire MCP:${nc} claude mcp add --transport http tickets_please ${HEALTH_URL%/healthz}/mcp"
