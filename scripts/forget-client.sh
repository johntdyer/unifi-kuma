#!/usr/bin/env bash
#
# forget-client.sh — remove one or more clients from a UniFi controller by
# MAC address via the legacy stamgr API, even when they don't show up
# anywhere in the UniFi UI. See the "A monitor keeps coming back after I
# delete it" section of the main README for why this is sometimes the only
# way to actually get rid of one.
#
# Mirrors internal/unifi/client.go's own login logic: tries the UniFi OS
# (UDM) login endpoint first and falls back to the classic Network
# Application endpoint automatically. UniFi OS additionally requires the
# X-Csrf-Token from the login response on every follow-up request — get
# this wrong (e.g. skip it, or reuse a stale one) and stamgr calls fail
# with 401/403 even though the cookie-based session itself is fine.
#
# Usage:
#   UNIFI_URL=https://192.168.1.1 UNIFI_USERNAME=admin UNIFI_PASSWORD=secret \
#     ./scripts/forget-client.sh aa:bb:cc:dd:ee:ff [11:22:33:44:55:66 ...]
#
# Or pass flags instead of env vars:
#   ./scripts/forget-client.sh -h https://192.168.1.1 -u admin -p secret \
#     aa:bb:cc:dd:ee:ff
#
# Flags:
#   -h URL     Controller base URL (or $UNIFI_URL)
#   -u USER    Username (or $UNIFI_USERNAME)
#   -p PASS    Password (or $UNIFI_PASSWORD)
#   -s SITE    Site name (default: "default", or $UNIFI_SITE)
#   -k         Skip TLS certificate verification (self-signed certs)
#   -n         Dry run — log in and show what would be sent, but don't delete
#
# Requires: curl. Nothing else.

set -euo pipefail

usage() {
    grep '^#' "$0" | sed -e 's/^#!.*//' -e 's/^# \{0,1\}//' | sed '/^$/d'
    exit "${1:-0}"
}

url="${UNIFI_URL:-}"
user="${UNIFI_USERNAME:-}"
pass="${UNIFI_PASSWORD:-}"
site="${UNIFI_SITE:-default}"
insecure=()
dry_run=false

while getopts ":h:u:p:s:kn" opt; do
    case "$opt" in
        h) url=$OPTARG ;;
        u) user=$OPTARG ;;
        p) pass=$OPTARG ;;
        s) site=$OPTARG ;;
        k) insecure=(-k) ;;
        n) dry_run=true ;;
        \?) echo "Unknown flag: -$OPTARG" >&2; usage 1 ;;
        :) echo "-$OPTARG requires a value" >&2; usage 1 ;;
    esac
done
shift $((OPTIND - 1))

macs=("$@")

if [[ -z "$url" || -z "$user" || -z "$pass" ]]; then
    echo "Missing controller URL, username, or password." >&2
    usage 1
fi
if [[ ${#macs[@]} -eq 0 ]]; then
    echo "No MAC addresses given." >&2
    usage 1
fi

url="${url%/}"

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
cookies="$workdir/cookies.txt"
headers="$workdir/headers.txt"
curl_opts=(--connect-timeout 10 --max-time 30)

# json_escape falls back to naive quoting if python3 isn't available —
# fine for typical UniFi credentials, but if yours contain a backslash or
# double quote, install python3 or edit the script to shell out to jq -Rs instead.
json_escape() { python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1" 2>/dev/null || printf '"%s"' "$1"; }
login_body="{\"username\":$(json_escape "$user"),\"password\":$(json_escape "$pass"),\"remember\":true}"

is_udm=true
status=$(curl -sS "${curl_opts[@]}" "${insecure[@]}" -c "$cookies" -D "$headers" -o "$workdir/login.json" \
    -w '%{http_code}' \
    -X POST "$url/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d "$login_body")

if [[ "$status" == "404" ]]; then
    echo "UniFi OS login endpoint not found — falling back to classic controller login." >&2
    is_udm=false
    login_body="{\"username\":$(json_escape "$user"),\"password\":$(json_escape "$pass")}"
    status=$(curl -sS "${curl_opts[@]}" "${insecure[@]}" -c "$cookies" -D "$headers" -o "$workdir/login.json" \
        -w '%{http_code}' \
        -X POST "$url/api/login" \
        -H 'Content-Type: application/json' \
        -d "$login_body")
fi

if [[ "$status" != "200" ]]; then
    echo "Login failed (HTTP $status). Response:" >&2
    cat "$workdir/login.json" >&2
    exit 1
fi

csrf=""
if $is_udm; then
    csrf=$(grep -i '^x-csrf-token:' "$headers" | awk '{print $2}' | tr -d '\r')
    if [[ -z "$csrf" ]]; then
        echo "Logged in, but no X-Csrf-Token header in the response — UniFi OS stamgr calls will likely fail." >&2
    fi
fi

echo "Logged in ($([ "$is_udm" = true ] && echo "UniFi OS" || echo "classic")), site=$site"

if $is_udm; then
    stamgr_url="$url/proxy/network/api/s/$site/cmd/stamgr"
else
    stamgr_url="$url/api/s/$site/cmd/stamgr"
fi

macs_json=$(printf '"%s",' "${macs[@]}")
macs_json="[${macs_json%,}]"
body="{\"cmd\":\"forget-sta\",\"macs\":${macs_json}}"

echo "Forgetting: ${macs[*]}"
if $dry_run; then
    echo "[dry run] would POST $stamgr_url"
    echo "[dry run] body: $body"
    exit 0
fi

csrf_header=()
if [[ -n "$csrf" ]]; then
    csrf_header=(-H "X-Csrf-Token: $csrf")
fi

response=$(curl -sS "${curl_opts[@]}" "${insecure[@]}" -b "$cookies" "${csrf_header[@]}" \
    -X POST "$stamgr_url" \
    -H 'Content-Type: application/json' \
    -d "$body")

echo "$response"

if [[ "$response" == *'"rc":"ok"'* ]]; then
    echo "Done — forgot ${#macs[@]} client(s)."
else
    echo "stamgr did not report success — check the response above." >&2
    exit 1
fi
