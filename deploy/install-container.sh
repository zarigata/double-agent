#!/usr/bin/env bash
# Installs the Double Agent control plane inside a Debian based LXC container
# that runs Docker. The script is idempotent.
#
# Usage: install-container.sh <binary-dir> [config-path]
set -euo pipefail

BIN_DIR="${1:?the directory that holds the built binaries}"
CONFIG_DIR="/etc/double-agent"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
ENV_FILE="${CONFIG_DIR}/double-agent.env"
DATA_DIR="/var/lib/double-agent"
UNIT="/etc/systemd/system/double-agentd.service"

install -d -m 0750 "${CONFIG_DIR}" "${DATA_DIR}"
install -d -m 0750 "${DATA_DIR}/state" "${DATA_DIR}/cache" "${DATA_DIR}/logs" "${DATA_DIR}/pki"

install -m 0755 "${BIN_DIR}/double-agentd" /usr/local/bin/double-agentd
install -m 0755 "${BIN_DIR}/double-agent" /usr/local/bin/double-agent

# The credentials never live in the configuration file: they are read from an
# environment file that only the root user can read.
if [ ! -f "${ENV_FILE}" ]; then
	cat > "${ENV_FILE}" <<'ENVEOF'
# The credentials of the Double Agent node. Keep this file at mode 0600.
DOUBLE_AGENT_ADMIN_PASSWORD=change-me-now
ENVEOF
	chmod 0600 "${ENV_FILE}"
fi

cat > "${UNIT}" <<'UNITEOF'
[Unit]
Description=Double Agent control plane
Documentation=https://github.com/zarigata/double-agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/double-agent/double-agent.env
ExecStart=/usr/local/bin/double-agentd serve --config /etc/double-agent/config.yaml
Restart=on-failure
RestartSec=5
User=root
LimitNOFILE=65536
# The daemon only needs to write inside its own data directory.
ProtectHome=yes
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
UNITEOF

systemctl daemon-reload
systemctl enable double-agentd.service >/dev/null
systemctl restart double-agentd.service
sleep 3
systemctl is-active double-agentd.service
