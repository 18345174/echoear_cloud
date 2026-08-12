#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-$(CDPATH= cd -- "$(dirname "$0")" && pwd)/.env}"

fail() {
  printf '[relay-check] ERROR: %s\n' "$*" >&2
  exit 1
}

env_value() {
  grep -E "^$1=" "$ENV_FILE" | tail -n1 | cut -d= -f2- || true
}

resolve_ipv4() {
  local host="$1"
  if command -v dig >/dev/null 2>&1; then
    dig +short "$host" A | grep -E '^[0-9]+(\.[0-9]+){3}$' | sort -u
    return
  fi
  if command -v getent >/dev/null 2>&1; then
    getent ahostsv4 "$host" | awk '{print $1}' | sort -u
    return
  fi
  fail "install dig or getent to validate relay DNS"
}

[[ -f "$ENV_FILE" ]] || fail "environment file not found: $ENV_FILE"

enabled="$(env_value ECHOEAR_RELAY_ENABLED)"
enabled="$(printf '%s' "$enabled" | tr '[:upper:]' '[:lower:]')"
if [[ "$enabled" != "true" && "$enabled" != "1" ]]; then
  printf '[relay-check] relay is disabled in %s\n' "$ENV_FILE"
  exit 0
fi

domain="$(env_value ECHOEAR_RELAY_DOMAIN)"
public_ip="$(env_value ECHOEAR_RELAY_PUBLIC_IP)"
auth_secret="$(env_value ECHOEAR_RELAY_AUTH_SECRET)"

[[ -n "$domain" ]] || fail "ECHOEAR_RELAY_DOMAIN is empty"
[[ -n "$public_ip" ]] || fail "ECHOEAR_RELAY_PUBLIC_IP is empty"
[[ -n "$auth_secret" ]] || fail "ECHOEAR_RELAY_AUTH_SECRET is empty"

base_ips="$(resolve_ipv4 "$domain")"
wildcard_host="relay-check-$(date +%s).$domain"
wildcard_ips="$(resolve_ipv4 "$wildcard_host")"

printf '[relay-check] base %s -> %s\n' "$domain" "${base_ips:-<none>}"
printf '[relay-check] wildcard %s -> %s\n' "$wildcard_host" "${wildcard_ips:-<none>}"

grep -qx "$public_ip" <<<"$base_ips" || fail "$domain does not resolve to $public_ip"
grep -qx "$public_ip" <<<"$wildcard_ips" || fail "*.$domain does not resolve to $public_ip"

issue_status="$(curl --silent --output /dev/null --write-out '%{http_code}' \
  --connect-timeout 5 --max-time 10 "https://$domain/issue" || true)"
if [[ "$issue_status" != "405" ]]; then
  fail "https://$domain/issue returned HTTP ${issue_status:-000}"
fi

printf '[relay-check] relay registration endpoint is ready (HTTP %s)\n' "$issue_status"
printf '[relay-check] TCP/TLS is reachable; verify UDP 443 from a Controller runtime test.\n'
