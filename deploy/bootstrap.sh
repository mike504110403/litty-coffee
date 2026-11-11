#!/usr/bin/env bash
# litty-coffee 主機初始化（Ubuntu 24.04，Lightsail）。以 root 執行，可重複執行（每步先檢查現況）。
#
# 用法（從本機）：
#   ssh ubuntu@<主機> "sudo TS_AUTHKEY='<tskey>' DEPLOY_PUBKEY='<ssh-ed25519 ...>' bash -s" < deploy/bootstrap.sh
#
# 環境變數：
#   TS_AUTHKEY     Tailscale auth key（tag:litty-server）；主機尚未加入 tailnet 時必填，已加入則忽略
#   DEPLOY_PUBKEY  （選填）CI 部署用公鑰，加到 deploy 使用者的 authorized_keys（已存在則不重複加）
#
# 做的事：系統更新＋unattended-upgrades、2GB swap、Docker Engine＋compose plugin（官方 apt 源）、
# daemon.json 日誌輪替、deploy 使用者、Tailscale、/opt/litty/{dev,prod}、每日 pg_dump 備份、sshd 加固。
set -euo pipefail

log() { printf '\n==> %s\n' "$*"; }

if [[ $EUID -ne 0 ]]; then
  echo "請以 root 執行（sudo）" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
APT_OPTS=(-y -q -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)

# ---------------------------------------------------------------- 系統更新
log "系統更新"
apt-get update -q
apt-get "${APT_OPTS[@]}" upgrade
apt-get "${APT_OPTS[@]}" install ca-certificates curl gnupg unattended-upgrades cron

log "unattended-upgrades"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
systemctl enable --now unattended-upgrades

# ---------------------------------------------------------------- swap
log "swap 2GB"
if ! swapon --show=NAME --noheadings | grep -qx /swapfile; then
  if [[ ! -f /swapfile ]]; then
    fallocate -l 2G /swapfile
    chmod 600 /swapfile
    mkswap /swapfile
  fi
  swapon /swapfile
fi
grep -qE '^/swapfile\s' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab

log "sysctl"
cat > /etc/sysctl.d/90-litty.conf <<'EOF'
# swap 只當安全網
vm.swappiness = 10
# dev 的 web 綁在 Tailscale IP；開機時 tailscale0 還沒拿到 IP 也能先綁上
net.ipv4.ip_nonlocal_bind = 1
EOF
sysctl --system >/dev/null

# ---------------------------------------------------------------- Docker
log "Docker Engine（官方 apt 源）"
for pkg in docker.io docker-doc docker-compose docker-compose-v2 podman-docker containerd runc; do
  if dpkg -s "$pkg" >/dev/null 2>&1; then
    apt-get "${APT_OPTS[@]}" remove "$pkg"
  fi
done
install -m 0755 -d /etc/apt/keyrings
if [[ ! -s /etc/apt/keyrings/docker.asc ]]; then
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
fi
# shellcheck disable=SC1091
. /etc/os-release
cat > /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${UBUNTU_CODENAME:-$VERSION_CODENAME}
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
apt-get update -q
apt-get "${APT_OPTS[@]}" install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

log "daemon.json 日誌輪替"
DAEMON_JSON='{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "5" },
  "live-restore": true
}'
mkdir -p /etc/docker
if [[ ! -f /etc/docker/daemon.json ]] || [[ "$(cat /etc/docker/daemon.json)" != "$DAEMON_JSON" ]]; then
  printf '%s\n' "$DAEMON_JSON" > /etc/docker/daemon.json
  systemctl restart docker
fi
systemctl enable --now docker containerd

# ---------------------------------------------------------------- deploy 使用者
log "deploy 使用者"
if ! id deploy >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash deploy
fi
usermod -aG docker deploy
passwd -l deploy >/dev/null   # 鎖密碼：只能用 SSH key 登入
install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
touch /home/deploy/.ssh/authorized_keys
chown deploy:deploy /home/deploy/.ssh/authorized_keys
chmod 600 /home/deploy/.ssh/authorized_keys
if [[ -n "${DEPLOY_PUBKEY:-}" ]] && ! grep -qxF "$DEPLOY_PUBKEY" /home/deploy/.ssh/authorized_keys; then
  printf '%s\n' "$DEPLOY_PUBKEY" >> /home/deploy/.ssh/authorized_keys
fi

# ---------------------------------------------------------------- Tailscale
log "Tailscale"
if ! command -v tailscale >/dev/null 2>&1; then
  curl -fsSL https://tailscale.com/install.sh | sh
fi
systemctl enable --now tailscaled
if tailscale status --json 2>/dev/null | grep -q '"BackendState": *"Running"'; then
  echo "已在 tailnet：$(tailscale ip -4 | head -n1)"
else
  if [[ -z "${TS_AUTHKEY:-}" ]]; then
    echo "主機尚未加入 tailnet，請以 TS_AUTHKEY 環境變數提供 auth key 後重跑" >&2
    exit 1
  fi
  tailscale up --authkey="$TS_AUTHKEY" --hostname=litty-server --advertise-tags=tag:litty-server
  echo "Tailscale IP：$(tailscale ip -4 | head -n1)"
fi

# ---------------------------------------------------------------- 目錄
log "/opt/litty"
install -d -m 755 -o root -g root /opt/litty
for env in dev prod; do
  install -d -m 700 -o deploy -g deploy "/opt/litty/$env"
done
install -d -m 700 -o root -g root /opt/litty/backups /opt/litty/backups/dev /opt/litty/backups/prod

# ---------------------------------------------------------------- 備份
log "每日 pg_dump 備份（保留 14 天）"
cat > /usr/local/sbin/litty-pg-backup <<'EOF'
#!/usr/bin/env bash
# 每日備份 dev／prod 的 PostgreSQL（custom format，可用 pg_restore 還原）；保留 14 天。
set -uo pipefail
status=0
stamp=$(date -u +%Y%m%dT%H%M%SZ)
for env in dev prod; do
  dir=/opt/litty/backups/$env
  cid=$(docker ps -q \
    --filter "label=com.docker.compose.project=litty-$env" \
    --filter "label=com.docker.compose.service=postgres" | head -n1)
  if [[ -z "$cid" ]]; then
    echo "[$env] 找不到執行中的 postgres 容器，略過"
    continue
  fi
  out="$dir/litty-$env-$stamp.dump"
  if docker exec "$cid" pg_dump -U litty -d litty -Fc > "$out.part" && [[ -s "$out.part" ]]; then
    mv "$out.part" "$out"
    chmod 600 "$out"
    echo "[$env] ok $(du -h "$out" | cut -f1) $out"
  else
    rm -f "$out.part"
    echo "[$env] 備份失敗" >&2
    status=1
  fi
  find "$dir" -maxdepth 1 -type f -name "litty-$env-*.dump" -mtime +13 -delete
done
exit $status
EOF
chmod 750 /usr/local/sbin/litty-pg-backup
# 19:30 UTC＝台北 03:30
cat > /etc/cron.d/litty-backup <<'EOF'
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
30 19 * * * root /usr/local/sbin/litty-pg-backup >> /var/log/litty-backup.log 2>&1
EOF
chmod 644 /etc/cron.d/litty-backup
cat > /etc/logrotate.d/litty-backup <<'EOF'
/var/log/litty-backup.log {
  monthly
  rotate 6
  compress
  missingok
  notifempty
}
EOF
systemctl enable --now cron

# ---------------------------------------------------------------- sshd 加固
log "sshd 加固"
# 檔名排在 cloud-init 的 60-cloudimg-settings.conf 前面（sshd 取第一個出現的值）
cat > /etc/ssh/sshd_config.d/10-litty-hardening.conf <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
PermitEmptyPasswords no
X11Forwarding no
MaxAuthTries 3
EOF
sshd -t
systemctl try-reload-or-restart ssh

log "完成"
docker --version
docker compose version
echo "Tailscale：$(tailscale ip -4 | head -n1)（hostname litty-server）"
echo "下一步：把 deploy/env/<env>.env.example 填成 /opt/litty/<env>/.env（owner deploy、chmod 600）"
