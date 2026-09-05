#!/usr/bin/env sh
# Renew an existing algo-tron certificate without rebuilding or restarting the
# application. Certbot's saved renewal configuration supplies the domain and
# Cloudflare plugin settings.
set -eu

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
command -v certbot >/dev/null 2>&1 || { echo "certbot is not installed" >&2; exit 1; }

certbot renew --non-interactive --deploy-hook "systemctl reload nginx"
