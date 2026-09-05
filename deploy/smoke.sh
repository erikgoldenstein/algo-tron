#!/usr/bin/env sh
# Small post-deploy check for the viewer edge. It deliberately uses curl rather
# than application internals, so it works against localhost, nginx, or the
# public URL.
set -eu

BASE="${1:-http://127.0.0.1:3000}"
failures=0
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

fail() { echo "  FAIL: $*" >&2; failures=$((failures + 1)); }
ok()   { echo "  ok:   $*"; }

echo "smoke: $BASE"

health="$(curl -fsS --max-time 10 "$BASE/healthz" 2>/dev/null || true)"
[ "$health" = "ok" ] && ok "health endpoint" || fail "/healthz did not return ok"

body="$(curl -fsS --max-time 10 "$BASE/" 2>/dev/null || true)"
printf '%s' "$body" | grep -q 'algo-tron' && ok "viewer page" || fail "viewer page was not served"

screen="$(curl -fsS --max-time 10 "$BASE/screen" 2>/dev/null || true)"
printf '%s' "$screen" | grep -q 'algo-tron' && ok "/screen page" || fail "/screen was not served"

code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$BASE/static/helpers.js" 2>/dev/null || true)"
[ "$code" = 200 ] && ok "static assets" || fail "/static/helpers.js returned $code"

curl -fsS -D "$tmp" -o /dev/null --max-time 10 "$BASE/" >/dev/null 2>&1 || true
for header in X-Content-Type-Options X-Frame-Options Referrer-Policy Content-Security-Policy; do
  grep -qi "^$header:" "$tmp" && ok "$header header" || fail "missing $header header"
done

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "smoke: passed"
