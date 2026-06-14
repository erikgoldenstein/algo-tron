#!/usr/bin/env bash
#
# deploy.sh — one-shot deploy of algo-tron onto a fresh Debian/Ubuntu or
#             Rocky/RHEL VM.
#
# nginx (IPv4 + IPv6) fronts both the HTTP viewer and the raw TCP game port;
# the app only binds localhost. The game port is forwarded with PROXY protocol
# so the app still sees real client IPs.
#
# Run as root, either from a checkout or straight from the internet (use bash,
# not sh — prompts are read from your terminal):
#
#   sudo ./deploy.sh --domain tron.example.com --cloudflare-token CF_TOKEN
#   curl -fsSL https://raw.githubusercontent.com/erikgoldenstein/algo-tron/main/deploy.sh | sudo bash
#
# Flags (anything omitted is asked for interactively):
#   --domain --cloudflare-token --tcp-port --view-port --repo --ref --no-firewall
#
# By default a host firewall (firewalld on Rocky/RHEL, ufw on Debian/Ubuntu) is
# installed and enabled, allowing only SSH and the public app ports — meant for
# a single directly-exposed host with nothing in front of it. Pass --no-firewall
# if an external/cloud firewall already guards the box.
#
# Security model: nginx and certbot run as root; the Cloudflare token lives only
# in /root/.secrets/cloudflare.ini (root-only, 600) and is reused automatically on
# redeploys, so it need not be re-entered. The game binary runs as the
# unprivileged 'tron' user (no password, no login shell, no sudo), so a
# compromise of the game process cannot read the token or escalate.

set -euo pipefail

if [ -z "${BASH_VERSION:-}" ]; then
  echo "error: run with bash, e.g.  curl -fsSL <url> | sudo bash" >&2
  exit 1
fi

# --- config (overridable by flags / env) -----------------------------------
DOMAIN=""
CLOUDFLARE_TOKEN=""
TCP_PORT="4000"         # public raw-TCP game port (nginx listens here)
VIEW_PORT="443"         # public HTTPS viewer port (nginx terminates TLS)
TCP_LOCAL_PORT="4001"   # localhost game port the app binds; nginx forwards to it
VIEW_LOCAL_PORT="3000"  # localhost viewer port the app binds; nginx forwards to it
SETUP_FIREWALL=1        # install+enable a host firewall (0 / --no-firewall to skip)

APP_USER="tron"
APP_HOME="/opt/algo-tron"
DATA_DIR="/var/lib/algo-tron"
BIN="$APP_HOME/algo-tron"
CF_INI="/root/.secrets/cloudflare.ini"  # saved Cloudflare token (root-only, 600)

# Source to build. Used only when not run from a checkout.
REPO_SLUG="${REPO_SLUG:-erikgoldenstein/algo-tron}"
REPO_REF="${REPO_REF:-main}"

# Build from the checkout the script lives in, or "" to download the source.
_src="${BASH_SOURCE[0]:-}"
if [ -n "$_src" ] && [ -e "$_src" ]; then
  REPO_DIR="$(cd "$(dirname "$_src")" && pwd)"
else
  REPO_DIR=""
fi

# --- helpers ---------------------------------------------------------------
log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
err() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# True only when /dev/tty can actually be opened for reading. A bare [ -r /dev/tty ]
# is not enough: under a non-interactive SSH command the device node exists and
# passes -r, but opening it fails with ENXIO (no controlling terminal).
have_tty() { ( : < /dev/tty ) 2>/dev/null; }

# Read into a variable from the terminal — works even when the script itself is
# on stdin (curl | bash).  prompt <varname> <message> [silent]
prompt() {
  local var="$1" msg="$2" silent="${3:-}" val
  have_tty || err "no value for '$var' and no terminal to ask; pass it as a flag"
  if [ "$silent" = silent ]; then
    read -rsp "$msg" val < /dev/tty; printf '\n' > /dev/tty
  else
    read -rp "$msg" val < /dev/tty
  fi
  printf -v "$var" '%s' "$val"
}

# --- steps -----------------------------------------------------------------
parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --domain)           DOMAIN="$2"; shift 2 ;;
      --cloudflare-token) CLOUDFLARE_TOKEN="$2"; shift 2 ;;
      --tcp-port)         TCP_PORT="$2"; shift 2 ;;
      --view-port)        VIEW_PORT="$2"; shift 2 ;;
      --repo)             REPO_SLUG="$2"; shift 2 ;;
      --ref)              REPO_REF="$2"; shift 2 ;;
      --no-firewall)      SETUP_FIREWALL=0; shift ;;
      -h|--help)          grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
      *) err "unknown argument: $1 (try --help)" ;;
    esac
  done
}

preflight() {
  [ "$(id -u)" -eq 0 ] || err "must run as root (try: sudo ./deploy.sh)"
  if command -v apt-get >/dev/null 2>&1; then PKG=apt
  elif command -v dnf >/dev/null 2>&1; then PKG=dnf
  else err "unsupported distro: need apt-get (Debian/Ubuntu) or dnf (Rocky/RHEL)"; fi
}

collect_input() {
  [ -n "$DOMAIN" ]           || prompt DOMAIN "Domain (e.g. tron.example.com): "
  [ -n "$DOMAIN" ]           || err "domain is required"

  # On redeploys, reuse the token saved on a previous run so it need not be
  # re-entered (a flag still overrides it).
  if [ -z "$CLOUDFLARE_TOKEN" ] && [ -r "$CF_INI" ]; then
    CLOUDFLARE_TOKEN="$(sed -n 's/^[[:space:]]*dns_cloudflare_api_token[[:space:]]*=[[:space:]]*//p' "$CF_INI")"
    [ -n "$CLOUDFLARE_TOKEN" ] && log "Reusing saved Cloudflare token from $CF_INI"
  fi
  [ -n "$CLOUDFLARE_TOKEN" ] || prompt CLOUDFLARE_TOKEN "Cloudflare API token (Zone:DNS:Edit): " silent
  [ -n "$CLOUDFLARE_TOKEN" ] || err "cloudflare token is required"

  # Ports have defaults; only ask when there is a terminal to ask at.
  if have_tty; then
    prompt _in "Raw TCP game port [$TCP_PORT]: ";  TCP_PORT="${_in:-$TCP_PORT}"
    prompt _in "HTTPS viewer port [$VIEW_PORT]: "; VIEW_PORT="${_in:-$VIEW_PORT}"
  fi

  # What the viewer UI displays (omit :443 when standard).
  if [ "$VIEW_PORT" = 443 ]; then PUBLIC_VIEW="$DOMAIN"; else PUBLIC_VIEW="$DOMAIN:$VIEW_PORT"; fi
}

install_packages() {
  log "Installing system packages"
  if [ "$PKG" = apt ]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    # libnginx-mod-stream provides the stream{} module that fronts the game port.
    # tar+gzip are needed to unpack the Go toolchain and the source tarball.
    apt-get install -y -qq nginx libnginx-mod-stream certbot python3-certbot-dns-cloudflare curl ca-certificates tar gzip >/dev/null
  else
    # certbot and its Cloudflare plugin live in EPEL on Rocky/RHEL.
    dnf install -y -q epel-release >/dev/null
    # nginx-mod-stream provides (and auto-loads) the stream{} module that fronts
    # the game port; tar+gzip unpack the Go toolchain and the source tarball.
    dnf install -y -q nginx nginx-mod-stream certbot python3-certbot-dns-cloudflare curl ca-certificates tar gzip >/dev/null
  fi
}

fetch_source() {
  if [ -n "$REPO_DIR" ] && [ -f "$REPO_DIR/go.mod" ] && [ -d "$REPO_DIR/cmd/algo-tron" ]; then
    log "Building from local checkout: $REPO_DIR"
    return
  fi
  log "Downloading source from github.com/$REPO_SLUG@$REPO_REF"
  REPO_DIR="$(mktemp -d)"
  curl -fsSL "https://github.com/$REPO_SLUG/archive/refs/heads/$REPO_REF.tar.gz" \
    | tar -xz -C "$REPO_DIR" --strip-components=1
  [ -f "$REPO_DIR/go.mod" ] || err "downloaded source looks wrong (no go.mod) — check --repo/--ref"
}

install_go() {
  command -v go >/dev/null 2>&1 && return
  # A Go installed by a previous run persists under /usr/local/go but isn't on
  # PATH in a fresh shell; reuse it instead of re-downloading on every redeploy.
  if [ -x /usr/local/go/bin/go ]; then export PATH="/usr/local/go/bin:$PATH"; return; fi
  local ver arch
  # go.mod's "go" directive may be only major.minor (e.g. 1.26), but the toolchain
  # tarball needs a full version (go1.26.0). Prefer an explicit toolchain line;
  # otherwise append .0 when the patch level is missing.
  ver="$(awk '/^toolchain go/{sub(/^go/,"",$2); print $2; exit}' "$REPO_DIR/go.mod")"
  [ -n "$ver" ] || ver="$(awk '/^go /{print $2; exit}' "$REPO_DIR/go.mod")"
  case "$ver" in *.*.*) ;; *.*) ver="$ver.0" ;; esac
  case "$(uname -m)" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) err "unsupported CPU arch for auto Go install: $(uname -m) — install Go $ver manually" ;;
  esac
  log "Installing Go $ver ($arch)"
  curl -fsSL "https://go.dev/dl/go${ver}.linux-${arch}.tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz
  export PATH="/usr/local/go/bin:$PATH"
}

create_user() {
  if ! id "$APP_USER" >/dev/null 2>&1; then
    log "Creating system user '$APP_USER'"
    useradd --system --home "$APP_HOME" --shell "$(command -v nologin || echo /usr/sbin/nologin)" "$APP_USER"
  fi
  install -d -o "$APP_USER" -g "$APP_USER" "$APP_HOME" "$DATA_DIR"
}

build() {
  log "Building algo-tron"
  ( cd "$REPO_DIR" && go build -o "$BIN" ./cmd/algo-tron )
  chown "$APP_USER:$APP_USER" "$BIN"
}

issue_cert() {
  log "Obtaining TLS certificate for $DOMAIN"
  install -d -m 700 "$(dirname "$CF_INI")"
  ( umask 077; printf 'dns_cloudflare_api_token = %s\n' "$CLOUDFLARE_TOKEN" > "$CF_INI" )
  certbot certonly --non-interactive --agree-tos --register-unsafely-without-email \
    --dns-cloudflare --dns-cloudflare-credentials "$CF_INI" \
    -d "$DOMAIN" --deploy-hook "systemctl reload nginx"
}

# nginx binds non-standard ports and proxies to localhost; SELinux blocks both
# unless we allow it. No-op where SELinux is absent or disabled.
selinux_allow() {
  command -v getenforce >/dev/null 2>&1 && [ "$(getenforce)" != Disabled ] || return 0
  log "Configuring SELinux for nginx"
  setsebool -P httpd_can_network_connect 1 || true
  command -v semanage >/dev/null 2>&1 || return 0
  local p
  for p in "$VIEW_PORT" "$TCP_PORT"; do
    case "$p" in 80|443) ;; *) semanage port -a -t http_port_t -p tcp "$p" 2>/dev/null || true ;; esac
  done
}

configure_nginx() {
  log "Configuring nginx"
  # conf.d/*.conf is included inside http{}; server_tokens here applies to every
  # server block, hiding the nginx version (and name) from clients.
  cat > /etc/nginx/conf.d/algo-tron.conf <<EOF
server_tokens off;

server {
  listen 80;
  listen [::]:80;
  server_name $DOMAIN;
  return 301 https://\$host\$request_uri;
}

server {
  listen $VIEW_PORT ssl http2;
  listen [::]:$VIEW_PORT ssl http2;
  server_name $DOMAIN;

  ssl_certificate     /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;

  location / {
    proxy_pass http://127.0.0.1:$VIEW_LOCAL_PORT;
    proxy_http_version 1.1;
    proxy_set_header Upgrade \$http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_hide_header X-Powered-By;
    proxy_hide_header Server;
  }
}
EOF

  # stream{} must live at the top level of nginx.conf (not inside http{}), so it
  # goes in its own file that we include once from the end of nginx.conf.
  cat > /etc/nginx/algo-tron-stream.conf <<EOF
stream {
  upstream algo_tron_game {
    server 127.0.0.1:$TCP_LOCAL_PORT;
  }
  server {
    listen $TCP_PORT;
    listen [::]:$TCP_PORT;
    proxy_pass algo_tron_game;
    proxy_protocol on;
  }
}
EOF
  grep -q 'algo-tron-stream.conf' /etc/nginx/nginx.conf \
    || printf '\ninclude /etc/nginx/algo-tron-stream.conf;\n' >> /etc/nginx/nginx.conf

  selinux_allow
  nginx -t
  systemctl enable --now nginx >/dev/null 2>&1 || true
  systemctl reload nginx
}

install_service() {
  log "Installing systemd service"
  cat > /etc/systemd/system/algo-tron.service <<EOF
[Unit]
Description=algo-tron game server
After=network-online.target
Wants=network-online.target

[Service]
User=$APP_USER
Group=$APP_USER
ExecStart=$BIN \\
  -tcp 127.0.0.1:$TCP_LOCAL_PORT \\
  -view 127.0.0.1:$VIEW_LOCAL_PORT \\
  -proxy-protocol \\
  -public-tcp $DOMAIN:$TCP_PORT \\
  -public-view $PUBLIC_VIEW \\
  -public-view-scheme https \\
  -data-dir $DATA_DIR
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now algo-tron
  systemctl restart algo-tron
}

# Install and enable a host firewall that allows only SSH and the public app
# ports. Default on, for a single directly-exposed host; skip with --no-firewall.
setup_firewall() {
  if [ "$SETUP_FIREWALL" != 1 ]; then
    log "Skipping host firewall (--no-firewall)"
    return 0
  fi

  # Detect the SSH port(s) actually in use so enabling the firewall can't lock
  # us out — covers non-standard ports. Falls back to 22.
  local ssh_ports app_ports p
  ssh_ports="$(sshd -T 2>/dev/null | awk '/^port /{print $2}')"
  [ -n "$ssh_ports" ] || ssh_ports=22
  app_ports="80 $VIEW_PORT $TCP_PORT"

  if [ "$PKG" = apt ]; then
    log "Configuring host firewall (ufw)"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ufw >/dev/null
    for p in $ssh_ports $app_ports; do ufw allow "$p/tcp" >/dev/null; done
    ufw --force enable >/dev/null
  else
    log "Configuring host firewall (firewalld)"
    dnf install -y -q firewalld >/dev/null
    systemctl enable --now firewalld >/dev/null 2>&1
    for p in $ssh_ports $app_ports; do firewall-cmd --permanent --add-port="$p/tcp" >/dev/null; done
    firewall-cmd --reload >/dev/null
  fi
}

summary() {
  log "Done."
  echo "  Viewer:     https://$PUBLIC_VIEW"
  echo "  Game (TCP): $DOMAIN:$TCP_PORT"
  echo "  Logs:       journalctl -u algo-tron -f"
}

main() {
  parse_args "$@"
  preflight
  collect_input
  log "Deploying $DOMAIN  (TCP game :$TCP_PORT, HTTPS viewer :$VIEW_PORT)"
  install_packages
  fetch_source
  install_go
  create_user
  build
  issue_cert
  configure_nginx
  install_service
  setup_firewall
  summary
}

main "$@"
