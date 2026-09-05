#!/usr/bin/env bash
#
# deploy.sh — provision or redeploy algo-tron on a Debian/Ubuntu or Rocky/RHEL
#             VM. Routine deployments should normally go through CI; this
#             script is useful for first setup and deliberate manual releases.
#
# nginx (IPv4 + IPv6) fronts both the HTTP viewer and the raw TCP game port;
# the app only binds localhost. The game port is forwarded with PROXY protocol
# so the app still sees real client IPs.
#
# Run as root, either from a checkout or straight from the internet (use bash,
# not sh — prompts are read from your terminal):
#
#   sudo ./deploy.sh
#   sudo ./deploy.sh --dry-run
#   sudo ./deploy.sh --rollback
#   curl -fsSL https://raw.githubusercontent.com/erikgoldenstein/algo-tron/main/deploy.sh | sudo bash -s -- --domain tron.example.com
#
# Flags (anything omitted is asked for interactively; the production domain and
# saved Cloudflare token are reused by default):
#   --domain --cloudflare-token --acme-email --tcp-port --view-port --repo --ref
#   --deploy-only --dry-run --allow-dirty --rollback --no-backup --backup-dir --backup-keep
#   --no-firewall --no-auditd --no-fail2ban --no-upgrades --no-hardening
#   --no-metrics --reset-metrics-creds
#
# Certificate renewal is separate: `deploy/renew-cert.sh` calls Certbot's saved
# renewal configuration and only reloads nginx when a certificate changed.
#
# By default a host firewall (firewalld on Rocky/RHEL, ufw on Debian/Ubuntu) is
# installed and enabled, allowing only SSH and the public app ports — meant for
# a single directly-exposed host with nothing in front of it. Pass --no-firewall
# if an external/cloud firewall already guards the box.
#
# By default Prometheus /metrics is exposed via nginx at https://<domain>/metrics
# protected by HTTP basic auth. The app binds the metrics endpoint on a separate
# localhost-only port (127.0.0.1:9100) and nginx fronts it with auth. Credentials
# are auto-generated on the first deploy and reused on every subsequent run; pass
# --reset-metrics-creds to roll them, or --no-metrics to skip the whole thing.
# The credentials are printed at the end of the deploy and logged once by
# algo-tron on startup so `journalctl -u algo-tron` can recover them.
#
# Security model: nginx and certbot run as root; the Cloudflare token lives only
# in /root/.secrets/cloudflare.ini (root-only, 600) and is reused automatically on
# redeploys, so it need not be re-entered. The metrics credentials live next to
# it at /root/.secrets/algo-tron-metrics.{creds,env}. The game binary runs as the
# unprivileged 'tron' user (no password, no login shell, no sudo), so a
# compromise of the game process cannot read the token or escalate.

set -euo pipefail

if [ -z "${BASH_VERSION:-}" ]; then
  echo "error: run with bash, e.g.  curl -fsSL <url> | sudo bash" >&2
  exit 1
fi

# --- config (overridable by flags / env) -----------------------------------
DOMAIN="${DOMAIN:-tron.erik.gdn}"
CLOUDFLARE_TOKEN=""
ACME_EMAIL="${ACME_EMAIL:-}"
DRY_RUN=0
DEPLOY_ONLY=0
ALLOW_DIRTY=0
ROLLBACK=0
SETUP_BACKUP=1
BACKUP_DIR="/var/backups/algo-tron"
BACKUP_KEEP=7
TCP_PORT="4000"         # public raw-TCP game port (nginx listens here)
VIEW_PORT="443"         # public HTTPS viewer port (nginx terminates TLS)
TCP_PORT_SET=""         # set by --tcp-port, suppresses the interactive prompt
VIEW_PORT_SET=""        # set by --view-port, suppresses the interactive prompt
TCP_LOCAL_PORT="4001"   # localhost game port the app binds; nginx forwards to it
VIEW_LOCAL_PORT="3000"  # localhost viewer port the app binds; nginx forwards to it
METRICS_LOCAL_PORT="9100" # localhost /metrics port the app binds; nginx fronts it with basic auth
SETUP_FIREWALL=1        # install+enable a host firewall (0 / --no-firewall to skip)
SETUP_METRICS=1         # expose /metrics via nginx with basic auth (0 / --no-metrics to skip)
RESET_METRICS_CREDS=0   # 1 / --reset-metrics-creds to roll the saved metrics password
SETUP_AUDITD=1          # install+enable auditd
SETUP_FAIL2BAN=1        # install+enable fail2ban's sshd jail
SETUP_UPGRADES=1        # install+enable unattended security updates

APP_USER="tron"
APP_HOME="/opt/algo-tron"
DATA_DIR="/var/lib/algo-tron"
BIN="$APP_HOME/algo-tron"
RELEASE_DIR="$APP_HOME/releases"
RELEASE_META="$APP_HOME/release"
DB_PATH="$DATA_DIR/players.db"
CF_INI="/root/.secrets/cloudflare.ini"  # saved Cloudflare token (root-only, 600)
GEO_DIR="$DATA_DIR/geo"                  # GeoLite2 .mmdb files (downloaded license-less)
METRICS_CREDS_FILE="/root/.secrets/algo-tron-metrics.creds" # "metrics:<plaintext>" (root, 600)
METRICS_ENV_FILE="/root/.secrets/algo-tron-metrics.env"     # ALGO_TRON_METRICS_CREDS=... for systemd
METRICS_HTPASSWD="/etc/nginx/algo-tron-metrics.htpasswd"    # bcrypt hash nginx reads

# Source to build. Used only when not run from a checkout.
REPO_SLUG="${REPO_SLUG:-erikgoldenstein/algo-tron}"
REPO_REF="${REPO_REF:-main}"
REPO_REF_SET=""
BUILD_COMMIT="unknown"

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
      --domain)           [ $# -ge 2 ] || err "--domain needs a value"; DOMAIN="$2"; shift 2 ;;
      --cloudflare-token) [ $# -ge 2 ] || err "--cloudflare-token needs a value"; CLOUDFLARE_TOKEN="$2"; shift 2 ;;
      --acme-email)       [ $# -ge 2 ] || err "--acme-email needs a value"; ACME_EMAIL="$2"; shift 2 ;;
      --tcp-port)         [ $# -ge 2 ] || err "--tcp-port needs a value"; TCP_PORT="$2"; TCP_PORT_SET=1; shift 2 ;;
      --view-port)        [ $# -ge 2 ] || err "--view-port needs a value"; VIEW_PORT="$2"; VIEW_PORT_SET=1; shift 2 ;;
      --repo)             [ $# -ge 2 ] || err "--repo needs a value"; REPO_SLUG="$2"; shift 2 ;;
      --ref)              [ $# -ge 2 ] || err "--ref needs a value"; REPO_REF="$2"; REPO_REF_SET=1; shift 2 ;;
      --dry-run)          DRY_RUN=1; shift ;;
      --deploy-only)      DEPLOY_ONLY=1; shift ;;
      --allow-dirty)      ALLOW_DIRTY=1; shift ;;
      --rollback)         ROLLBACK=1; shift ;;
      --no-backup)        SETUP_BACKUP=0; shift ;;
      --backup-dir)       [ $# -ge 2 ] || err "--backup-dir needs a value"; BACKUP_DIR="$2"; shift 2 ;;
      --backup-keep)      [ $# -ge 2 ] || err "--backup-keep needs a value"; BACKUP_KEEP="$2"; shift 2 ;;
      --no-firewall)      SETUP_FIREWALL=0; shift ;;
      --no-auditd)        SETUP_AUDITD=0; shift ;;
      --no-fail2ban)      SETUP_FAIL2BAN=0; shift ;;
      --no-upgrades)      SETUP_UPGRADES=0; shift ;;
      --no-hardening)     SETUP_FIREWALL=0; SETUP_AUDITD=0; SETUP_FAIL2BAN=0; SETUP_UPGRADES=0; shift ;;
      --no-metrics)       SETUP_METRICS=0; shift ;;
      --reset-metrics-creds) RESET_METRICS_CREDS=1; shift ;;
      -h|--help)          grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
      *) err "unknown argument: $1 (try --help)" ;;
    esac
  done
}

preflight() {
  [ "$(id -u)" -eq 0 ] || err "must run as root (try: sudo ./deploy.sh)"
  [ "$ROLLBACK$DEPLOY_ONLY" != "11" ] || err "--rollback and --deploy-only cannot be combined"
  [ "$ROLLBACK$DRY_RUN" != "11" ] || err "--rollback and --dry-run cannot be combined"
  [ "$DEPLOY_ONLY$DRY_RUN" != "11" ] || err "--deploy-only and --dry-run cannot be combined"
  if [ "$ROLLBACK" = 1 ]; then
    case "$BACKUP_DIR" in ''|/) err "--backup-dir must be a dedicated directory, not /" ;; esac
    return 0
  fi
  if command -v apt-get >/dev/null 2>&1; then PKG=apt
  elif command -v dnf >/dev/null 2>&1; then PKG=dnf
  else err "unsupported distro: need apt-get (Debian/Ubuntu) or dnf (Rocky/RHEL)"; fi

  case "$BACKUP_KEEP" in ''|*[!0-9]*) err "--backup-keep must be a non-negative integer" ;; esac
  [ "$BACKUP_KEEP" -gt 0 ] || err "--backup-keep must be greater than zero"
  case "$BACKUP_DIR" in ''|/) err "--backup-dir must be a dedicated directory, not /" ;; esac
  case "$TCP_PORT:$VIEW_PORT" in *[!0-9:]*|*:|:*:) err "ports must be numeric" ;; esac

  if [ -n "$REPO_DIR" ] && [ -f "$REPO_DIR/go.mod" ] && [ -d "$REPO_DIR/cmd/algo-tron" ]; then
    command -v git >/dev/null 2>&1 || err "git is required when deploying from a checkout"
    if [ -n "$(git -C "$REPO_DIR" status --porcelain)" ]; then
      if [ "$ALLOW_DIRTY" = 1 ]; then
        log "working tree is dirty; deploying committed HEAD only (--allow-dirty)"
      else
        git -C "$REPO_DIR" status --short >&2
        err "working tree is not clean — commit first, or pass --allow-dirty"
      fi
    fi
    local source_ref=HEAD
    [ -n "$REPO_REF_SET" ] && source_ref="$REPO_REF"
    BUILD_COMMIT="$(git -C "$REPO_DIR" rev-parse --verify "$source_ref^{commit}" 2>/dev/null)" \
      || err "cannot resolve source ref '$source_ref'"
  fi
}

collect_input() {
  [ -n "$DOMAIN" ]           || prompt DOMAIN "Domain (e.g. tron.example.com): "
  [ -n "$DOMAIN" ]           || err "domain is required"
  case "$DOMAIN" in
    *[!A-Za-z0-9.-]*|.*|*.|*-*-) err "domain must contain only hostname characters" ;;
  esac

  if [ "$DRY_RUN" = 1 ]; then
    if [ "$VIEW_PORT" = 443 ]; then PUBLIC_VIEW="$DOMAIN"; else PUBLIC_VIEW="$DOMAIN:$VIEW_PORT"; fi
    return 0
  fi

  # On redeploys, reuse the token saved on a previous run so it need not be
  # re-entered (a flag still overrides it).
  if [ -z "$CLOUDFLARE_TOKEN" ] && [ -r "$CF_INI" ]; then
    CLOUDFLARE_TOKEN="$(sed -n 's/^[[:space:]]*dns_cloudflare_api_token[[:space:]]*=[[:space:]]*//p' "$CF_INI")"
    [ -n "$CLOUDFLARE_TOKEN" ] && log "Reusing saved Cloudflare token from $CF_INI"
  fi
  [ -n "$CLOUDFLARE_TOKEN" ] || prompt CLOUDFLARE_TOKEN "Cloudflare API token (Zone:DNS:Edit): " silent
  [ -n "$CLOUDFLARE_TOKEN" ] || err "cloudflare token is required"

  # Ports have defaults; only ask when there is a terminal to ask at and the
  # value was not already pinned by a flag.
  if have_tty; then
    [ -n "$TCP_PORT_SET" ]  || { prompt _in "Raw TCP game port [$TCP_PORT]: ";  TCP_PORT="${_in:-$TCP_PORT}"; }
    [ -n "$VIEW_PORT_SET" ] || { prompt _in "HTTPS viewer port [$VIEW_PORT]: "; VIEW_PORT="${_in:-$VIEW_PORT}"; }
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
    apt-get install -y -qq nginx libnginx-mod-stream certbot python3-certbot-dns-cloudflare curl ca-certificates tar gzip sqlite3 git >/dev/null
    if [ "$SETUP_AUDITD" = 1 ]; then apt-get install -y -qq auditd >/dev/null; fi
    if [ "$SETUP_FAIL2BAN" = 1 ]; then apt-get install -y -qq fail2ban >/dev/null; fi
    if [ "$SETUP_UPGRADES" = 1 ]; then apt-get install -y -qq unattended-upgrades >/dev/null; fi
  else
    # certbot and its Cloudflare plugin live in EPEL on Rocky/RHEL.
    dnf install -y -q epel-release >/dev/null
    # nginx-mod-stream provides (and auto-loads) the stream{} module that fronts
    # the game port; tar+gzip unpack the Go toolchain and the source tarball.
    dnf install -y -q nginx nginx-mod-stream certbot python3-certbot-dns-cloudflare curl ca-certificates tar gzip sqlite git >/dev/null
    if [ "$SETUP_AUDITD" = 1 ]; then dnf install -y -q audit >/dev/null; fi
    if [ "$SETUP_FAIL2BAN" = 1 ]; then
      dnf install -y -q fail2ban >/dev/null
      [ "$SETUP_FIREWALL" = 1 ] && dnf install -y -q fail2ban-firewalld >/dev/null 2>&1 || true
    fi
    if [ "$SETUP_UPGRADES" = 1 ]; then dnf install -y -q dnf-automatic >/dev/null; fi
  fi
}

fetch_source() {
  if [ -n "$REPO_DIR" ] && [ -f "$REPO_DIR/go.mod" ] && [ -d "$REPO_DIR/cmd/algo-tron" ]; then
    # Build an exported commit, never the working tree. This keeps editor
    # scratch files and uncommitted changes out of a manual deployment.
    local source_ref=HEAD
    [ -n "$REPO_REF_SET" ] && source_ref="$REPO_REF"
    local stage
    stage="$(mktemp -d)"
    git -C "$REPO_DIR" archive "$source_ref" | tar -x -C "$stage"
    REPO_DIR="$stage"
    log "Building committed source $BUILD_COMMIT"
    return
  fi
  log "Downloading source from github.com/$REPO_SLUG@$REPO_REF"
  REPO_DIR="$(mktemp -d)"
  git clone --quiet --depth 1 --branch "$REPO_REF" "https://github.com/$REPO_SLUG.git" "$REPO_DIR"
  [ -f "$REPO_DIR/go.mod" ] || err "downloaded source looks wrong (no go.mod) — check --repo/--ref"
  BUILD_COMMIT="$(git -C "$REPO_DIR" rev-parse HEAD)"
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
  install -d -o "$APP_USER" -g "$APP_USER" "$RELEASE_DIR"
  local staged
  staged="$(mktemp "$APP_HOME/.algo-tron.new.XXXXXX")"
  if ! ( cd "$REPO_DIR" && go build -ldflags "-X main.buildCommit=$BUILD_COMMIT" -o "$staged" ./cmd/algo-tron ); then
    rm -f "$staged"
    err "algo-tron build failed; the running binary was left untouched"
  fi
  chmod 0755 "$staged"
  chown "$APP_USER:$APP_USER" "$staged"
  if [ -x "$BIN" ]; then
    local previous="$RELEASE_DIR/algo-tron-release-$(date -u +%Y%m%dT%H%M%SZ)-$$.bin"
    cp -p "$BIN" "$previous"
    chown "$APP_USER:$APP_USER" "$previous"
  fi
  mv -f "$staged" "$BIN"
  chown "$APP_USER:$APP_USER" "$BIN"
  local meta_tmp="$RELEASE_META.tmp.$$"
  printf 'commit=%s\nbuilt_at=%s\n' "$BUILD_COMMIT" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$meta_tmp"
  chown "$APP_USER:$APP_USER" "$meta_tmp"
  chmod 0644 "$meta_tmp"
  mv -f "$meta_tmp" "$RELEASE_META"
  prune_releases
}

prune_releases() {
  local files=() file
  while IFS= read -r file; do files+=("$file"); done < <(
    find "$RELEASE_DIR" -maxdepth 1 -type f -name 'algo-tron-release-*.bin' -printf '%T@ %p\n' 2>/dev/null | sort -n | cut -d' ' -f2-
  )
  while [ "${#files[@]}" -gt 5 ]; do
    rm -f -- "${files[0]}"
    files=("${files[@]:1}")
  done
}

backup_state() {
  [ "$SETUP_BACKUP" = 1 ] || { log "Skipping data backup (--no-backup)"; return 0; }
  [ -f "$DB_PATH" ] || { log "No existing player database to back up"; return 0; }
  install -d -m 700 "$BACKUP_DIR"
  local work partial final
  work="$(mktemp -d)"
  partial="$BACKUP_DIR/.algo-tron-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz.partial"
  final="${partial%.partial}"
  if ! printf '.timeout 5000\n.backup %s\n' "$work/players.db" | sqlite3 "$DB_PATH"; then
    rm -rf "$work" "$partial"
    err "could not create a consistent SQLite backup; deployment stopped"
  fi
  if [ "$(sqlite3 "$work/players.db" 'PRAGMA integrity_check;' 2>/dev/null)" != "ok" ]; then
    rm -rf "$work" "$partial"
    err "SQLite backup failed its integrity check; deployment stopped"
  fi
  for name in secret admin-password; do
    [ -f "$DATA_DIR/$name" ] && cp -p "$DATA_DIR/$name" "$work/$name"
  done
  if ! tar -czf "$partial" -C "$work" .; then
    rm -rf "$work" "$partial"
    err "could not create the state backup archive; deployment stopped"
  fi
  tar -tzf "$partial" >/dev/null || {
    rm -rf "$work" "$partial"
    err "state backup archive could not be read; deployment stopped"
  }
  chmod 0600 "$partial"
  mv -f "$partial" "$final"
  rm -rf "$work"
  log "Saved state backup: $final"
  prune_backups
}

prune_backups() {
  local files=() file
  while IFS= read -r file; do files+=("$file"); done < <(
    find "$BACKUP_DIR" -maxdepth 1 -type f -name 'algo-tron-*.tar.gz' -printf '%T@ %p\n' 2>/dev/null | sort -n | cut -d' ' -f2-
  )
  while [ "${#files[@]}" -gt "$BACKUP_KEEP" ]; do
    rm -f -- "${files[0]}"
    files=("${files[@]:1}")
  done
}

latest_release() {
  find "$RELEASE_DIR" -maxdepth 1 -type f -name 'algo-tron-release-*.bin' -printf '%T@ %p\n' 2>/dev/null \
    | sort -n | tail -1 | cut -d' ' -f2-
}

prune_rollback_snapshots() {
  local files=() file
  while IFS= read -r file; do files+=("$file"); done < <(
    find "$RELEASE_DIR" -maxdepth 1 -type f -name 'algo-tron-rollback-*.bin' -printf '%T@ %p\n' 2>/dev/null | sort -n | cut -d' ' -f2-
  )
  while [ "${#files[@]}" -gt 5 ]; do
    rm -f -- "${files[0]}"
    files=("${files[@]:1}")
  done
}

rollback() {
  local previous
  previous="$(latest_release)"
  [ -n "$previous" ] && [ -x "$previous" ] || err "no previous binary release is available in $RELEASE_DIR"
  backup_state
  log "Rolling back to $(basename "$previous")"
  [ -x "$BIN" ] && cp -p "$BIN" "$RELEASE_DIR/algo-tron-rollback-$(date -u +%Y%m%dT%H%M%SZ)-$$.bin"
  prune_rollback_snapshots
  install -o "$APP_USER" -g "$APP_USER" -m 0755 "$previous" "$BIN"
  printf 'rollback_from=%s\nrolled_back_at=%s\n' "$previous" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$RELEASE_META"
  chown "$APP_USER:$APP_USER" "$RELEASE_META"
  systemctl restart algo-tron
  wait_healthy || err "rollback binary did not become healthy"
  smoke_local || err "rollback smoke test failed"
  log "Rollback complete"
}

# Download the GeoLite2 databases so client geo/IP lookups work. License-less by
# default (a redistributed mirror); set MAXMIND_LICENSE_KEY or GEO_DATABASE_URL
# to override. Idempotent (skips existing files) and best-effort: a download
# failure must never block the deploy.
setup_geo() {
  log "Setting up GeoLite2 databases in $GEO_DIR"
  install -d -o "$APP_USER" -g "$APP_USER" "$GEO_DIR"
  runuser -u "$APP_USER" -- "$BIN" -setup-geo -geo-dir "$GEO_DIR" \
    || log "geo database setup failed (non-fatal) — geo/IP lookups stay disabled"
}

# Generate (or reuse) the basic-auth credentials nginx uses to gate /metrics.
# Plaintext lives in /root/.secrets so a redeploy can reuse it without churning
# the password, and the deploy script can print it; nginx only needs the salted
# hash in /etc/nginx/algo-tron-metrics.htpasswd. METRICS_CREDS is exported for
# later steps that need to embed it (systemd env file, summary).
setup_metrics_creds() {
  [ "$SETUP_METRICS" = 1 ] || return 0
  install -d -m 700 "$(dirname "$METRICS_CREDS_FILE")"

  if [ "$RESET_METRICS_CREDS" = 1 ] || [ ! -s "$METRICS_CREDS_FILE" ]; then
    log "Generating new /metrics basic-auth credentials"
    local pass
    # Subshell with pipefail off: head closes the pipe after 32 bytes, which
    # makes tr exit with SIGPIPE (141); under the script's `set -o pipefail`
    # that would abort the whole deploy.
    pass="$(set +o pipefail; LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32)"
    ( umask 077; printf 'metrics:%s\n' "$pass" > "$METRICS_CREDS_FILE" )
  else
    log "Reusing saved /metrics basic-auth credentials from $METRICS_CREDS_FILE"
  fi

  METRICS_CREDS="$(cat "$METRICS_CREDS_FILE")"
  local user="${METRICS_CREDS%%:*}" pass="${METRICS_CREDS#*:}"

  # Env file consumed by the algo-tron systemd unit so the app can echo the
  # creds into journald on startup (auth itself is enforced by nginx).
  ( umask 077; printf 'ALGO_TRON_METRICS_CREDS=%s\n' "$METRICS_CREDS" > "$METRICS_ENV_FILE" )

  # Use openssl (already pulled in by certbot/nginx) to write the salted apr1
  # hash nginx's auth_basic expects, avoiding an apache2-utils/httpd-tools
  # dependency just for `htpasswd`.
  local hash
  hash="$(openssl passwd -apr1 "$pass")"
  ( umask 022; printf '%s:%s\n' "$user" "$hash" > "$METRICS_HTPASSWD.tmp" )
  mv "$METRICS_HTPASSWD.tmp" "$METRICS_HTPASSWD"
}

issue_cert() {
  log "Obtaining TLS certificate for $DOMAIN"
  install -d -m 700 "$(dirname "$CF_INI")"
  ( umask 077; printf 'dns_cloudflare_api_token = %s\n' "$CLOUDFLARE_TOKEN" > "$CF_INI" )
  local email_args=()
  if [ -n "$ACME_EMAIL" ]; then
    email_args=(-m "$ACME_EMAIL")
  else
    email_args=(--register-unsafely-without-email)
  fi
  certbot certonly --non-interactive --agree-tos --keep-until-expiring "${email_args[@]}" \
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
  # /metrics is gated by basic auth at the nginx layer and proxied to the
  # localhost-only listener the app binds for it. Skipped when --no-metrics is
  # set, in which case the path is not exposed at all.
  local metrics_block=""
  if [ "$SETUP_METRICS" = 1 ]; then
    metrics_block=$(cat <<EOF

  location = /metrics {
    auth_basic           "algo-tron metrics";
    auth_basic_user_file $METRICS_HTPASSWD;
    limit_req            zone=algo_tron_metrics_per_ip burst=3 nodelay;
    proxy_pass           http://127.0.0.1:$METRICS_LOCAL_PORT/metrics;
    proxy_set_header     Host \$host;
    proxy_hide_header    X-Powered-By;
    proxy_hide_header    Server;
  }
EOF
)
  fi

  # conf.d/*.conf is included inside http{}; server_tokens here applies to every
  # server block, hiding the nginx version (and name) from clients.
  cat > /etc/nginx/conf.d/algo-tron.conf <<EOF
server_tokens off;

map \$host \$algo_tron_valid_host {
  default 0;
  $DOMAIN 1;
  localhost 1;
  127.0.0.1 1;
  ::1 1;
}

limit_req_zone \$binary_remote_addr zone=algo_tron_per_ip:10m rate=20r/s;
limit_req_zone \$binary_remote_addr zone=algo_tron_metrics_per_ip:10m rate=12r/m;

server {
  listen 80;
  listen [::]:80;
  server_name $DOMAIN;
  if (\$algo_tron_valid_host = 0) { return 444; }
  return 301 https://\$host\$request_uri;
}

server {
  listen $VIEW_PORT ssl http2;
  listen [::]:$VIEW_PORT ssl http2;
  server_name $DOMAIN;

  if (\$algo_tron_valid_host = 0) { return 444; }

  add_header X-Content-Type-Options nosniff always;
  add_header X-Frame-Options DENY always;
  add_header Referrer-Policy no-referrer always;
  add_header Strict-Transport-Security "max-age=63072000; includeSubDomains" always;
  add_header Content-Security-Policy "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' https:" always;

  ssl_certificate     /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;
$metrics_block
  location / {
    limit_req zone=algo_tron_per_ip burst=1200 nodelay;
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
  # When metrics is enabled the app binds a localhost-only /metrics listener
  # (nginx enforces auth) and reads the credentials from an EnvironmentFile so
  # they can be echoed into journald on startup without leaking via `ps`.
  local metrics_flag="" env_line=""
  if [ "$SETUP_METRICS" = 1 ]; then
    metrics_flag="  -metrics 127.0.0.1:$METRICS_LOCAL_PORT \\
"
    env_line="EnvironmentFile=$METRICS_ENV_FILE"
  fi
  cat > /etc/systemd/system/algo-tron.service <<EOF
[Unit]
Description=algo-tron game server
After=network-online.target
Wants=network-online.target

[Service]
User=$APP_USER
Group=$APP_USER
$env_line
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=$DATA_DIR
CapabilityBoundingSet=
LockPersonality=true
RestrictSUIDSGID=true
ExecStart=$BIN \\
$metrics_flag  -tcp 127.0.0.1:$TCP_LOCAL_PORT \\
  -view 127.0.0.1:$VIEW_LOCAL_PORT \\
  -proxy-protocol \\
  -public-tcp $DOMAIN:$TCP_PORT \\
  -public-view $PUBLIC_VIEW \\
  -public-view-scheme https \\
  -data-dir $DATA_DIR \\
  -geo-dir $GEO_DIR
Restart=always
RestartSec=2
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable algo-tron >/dev/null
}

wait_healthy() {
  log "Waiting for algo-tron to become healthy"
  local waited=0
  while [ "$waited" -lt 60 ]; do
    if systemctl is-active --quiet algo-tron && curl -fsS --max-time 3 \
        "http://127.0.0.1:$VIEW_LOCAL_PORT/healthz" >/dev/null; then
      if [ "$SETUP_METRICS" = 0 ] || curl -fsS --max-time 3 \
          "http://127.0.0.1:$METRICS_LOCAL_PORT/metrics" >/dev/null; then
        return 0
      fi
    fi
    sleep 2
    waited=$((waited + 2))
  done
  systemctl status algo-tron --no-pager >&2 || true
  return 1
}

smoke_local() {
  [ -x "$REPO_DIR/deploy/smoke.sh" ] || return 0
  "$REPO_DIR/deploy/smoke.sh" "http://127.0.0.1:$VIEW_LOCAL_PORT"
}

restart_service() {
  log "Restarting algo-tron"
  systemctl restart algo-tron
  if ! wait_healthy; then
    log "New release failed health check; restoring the previous binary"
    rollback
    err "deployment rolled back after failed health check"
  fi
  if ! smoke_local; then
    log "New release failed the smoke test; restoring the previous binary"
    rollback
    err "deployment rolled back after failed smoke test"
  fi
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
  local ssh_ports app_ports ports p missing
  ssh_ports="$(sshd -T 2>/dev/null | awk '/^port /{print $2}')"
  [ -n "$ssh_ports" ] || ssh_ports=22
  app_ports="80 $VIEW_PORT $TCP_PORT"
  ports="$ssh_ports $app_ports"

  if [ "$PKG" = apt ]; then
    # Leave an already-active ufw alone when every required port is allowed, so
    # a redeploy doesn't churn the firewall.
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
      missing=0
      for p in $ports; do ufw status | grep -qw "$p/tcp" || missing=1; done
      [ "$missing" = 0 ] && { log "Host firewall (ufw) already configured — leaving it untouched"; return 0; }
    fi
    log "Configuring host firewall (ufw)"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ufw >/dev/null
    for p in $ports; do ufw allow "$p/tcp" >/dev/null; done
    ufw --force enable >/dev/null
  else
    # Same for an already-running firewalld with every required port open.
    if systemctl is-active --quiet firewalld 2>/dev/null; then
      missing=0
      for p in $ports; do firewall-cmd --query-port="$p/tcp" >/dev/null 2>&1 || missing=1; done
      [ "$missing" = 0 ] && { log "Host firewall (firewalld) already configured — leaving it untouched"; return 0; }
    fi
    log "Configuring host firewall (firewalld)"
    dnf install -y -q firewalld >/dev/null
    systemctl enable --now firewalld >/dev/null 2>&1
    for p in $ports; do firewall-cmd --permanent --add-port="$p/tcp" >/dev/null; done
    if firewall-cmd --permanent --query-service=cockpit >/dev/null 2>&1; then
      firewall-cmd --permanent --remove-service=cockpit >/dev/null
      log "Removed unused cockpit service from firewalld"
    fi
    firewall-cmd --reload >/dev/null
  fi
}

setup_auditd() {
  [ "$SETUP_AUDITD" = 1 ] || { log "Skipping auditd (--no-auditd)"; return 0; }
  log "Enabling auditd"
  systemctl enable --now auditd >/dev/null 2>&1 || log "auditd could not be started (non-fatal)"
}

setup_fail2ban() {
  [ "$SETUP_FAIL2BAN" = 1 ] || { log "Skipping fail2ban (--no-fail2ban)"; return 0; }
  log "Configuring fail2ban for SSH"
  install -d -m 755 /etc/fail2ban/jail.d
  cat > /etc/fail2ban/jail.d/99-algo-tron.local <<'EOF'
# Written by algo-tron deploy.sh. Edit the script, not this file.
[DEFAULT]
backend = systemd
bantime = 1h
findtime = 10m
maxretry = 5

[sshd]
enabled = true
EOF
  systemctl enable --now fail2ban >/dev/null 2>&1 || log "fail2ban could not be started (non-fatal)"
}

setup_unattended_upgrades() {
  [ "$SETUP_UPGRADES" = 1 ] || { log "Skipping automatic updates (--no-upgrades)"; return 0; }
  log "Configuring automatic security updates"
  if [ "$PKG" = apt ]; then
    install -d -m 755 /etc/apt/apt.conf.d
    cat > /etc/apt/apt.conf.d/20algo-tron-unattended <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
  else
    local conf=/etc/dnf/automatic.conf dir=/etc/systemd/system/dnf-automatic.timer.d
    sed -i 's/^apply_updates *=.*/apply_updates = yes/; s/^reboot *=.*/reboot = never/' "$conf"
    grep -q '^apply_updates = yes' "$conf" || err "could not enable dnf automatic updates"
    install -d -m 755 "$dir"
    cat > "$dir/override.conf" <<'EOF'
# Written by algo-tron deploy.sh. Edit the script, not this file.
[Timer]
OnCalendar=
OnCalendar=*-*-* 03:00:00 Europe/Berlin
RandomizedDelaySec=0
Persistent=true
EOF
    systemctl daemon-reload
    systemctl enable --now dnf-automatic.timer >/dev/null 2>&1 || log "dnf automatic updates could not be started (non-fatal)"
  fi
}

setup_hardening() {
  if [ "$SETUP_FIREWALL$SETUP_AUDITD$SETUP_FAIL2BAN$SETUP_UPGRADES" = "0000" ]; then
    log "Skipping host hardening"
    return 0
  fi
  setup_firewall
  setup_auditd
  setup_fail2ban
  setup_unattended_upgrades
}

dry_run_summary() {
  log "Dry run: no packages, files, certificates, firewall rules, backups, or services will be changed."
  echo "  Domain:     $DOMAIN"
  echo "  Source:     $REPO_SLUG@$REPO_REF${BUILD_COMMIT:+ ($BUILD_COMMIT)}"
  echo "  Viewer:     https://$PUBLIC_VIEW"
  echo "  Game (TCP): $DOMAIN:$TCP_PORT"
  echo "  Data:       $DATA_DIR"
  echo "  Backups:    $BACKUP_DIR (keep $BACKUP_KEEP)"
  echo "  Hardening:  firewall=$SETUP_FIREWALL auditd=$SETUP_AUDITD fail2ban=$SETUP_FAIL2BAN upgrades=$SETUP_UPGRADES"
}

deploy_only() {
  [ -f /etc/systemd/system/algo-tron.service ] || err "algo-tron.service is not installed; run a full provisioning deploy first"
  command -v sqlite3 >/dev/null 2>&1 || err "sqlite3 is required for the pre-deploy backup; install it or run a full deploy"
  if ! grep -q -- '-metrics 127.0.0.1:' /etc/systemd/system/algo-tron.service; then
    SETUP_METRICS=0
  fi
  log "Deploy-only mode: leaving nginx, certificates, firewall, and host hardening untouched"
  fetch_source
  install_go
  backup_state
  build
  restart_service
  echo "  Release:    $BUILD_COMMIT"
  echo "  Backup:     $BACKUP_DIR (keep $BACKUP_KEEP)"
}

summary() {
  log "Done."
  echo "  Release:    $BUILD_COMMIT"
  echo "  Viewer:     https://$PUBLIC_VIEW"
  echo "  Game (TCP): $DOMAIN:$TCP_PORT"
  echo "  Backup:     $BACKUP_DIR (keep $BACKUP_KEEP)"
  echo "  Rollback:   sudo $0 --rollback"
  if [ "$SETUP_METRICS" = 1 ]; then
    echo "  Metrics:    https://$PUBLIC_VIEW/metrics  (basic auth)"
    echo "    creds:    $METRICS_CREDS"
    echo "    saved at: $METRICS_CREDS_FILE  (also in journalctl -u algo-tron)"
  fi
  echo "  Logs:       journalctl -u algo-tron -f"
}

main() {
  parse_args "$@"
  preflight
  if [ "$ROLLBACK" = 1 ]; then
    # A rollback must work with units created by older deploys that did not
    # expose the metrics listener.
    SETUP_METRICS=0
    rollback
    return 0
  fi
  if [ "$DEPLOY_ONLY" = 1 ]; then
    deploy_only
    return 0
  fi
  collect_input
  if [ "$DRY_RUN" = 1 ]; then
    dry_run_summary
    return 0
  fi
  log "Deploying $DOMAIN  (TCP game :$TCP_PORT, HTTPS viewer :$VIEW_PORT)"
  install_packages
  fetch_source
  install_go
  create_user
  backup_state
  build
  setup_geo
  setup_metrics_creds
  issue_cert
  configure_nginx
  install_service
  setup_hardening
  restart_service
  summary
}

main "$@"
