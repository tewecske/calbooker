#!/bin/sh
# Registers (or re-registers) calbooker in the running Hydra container of the
# goweb deployment and prints the two settings calbooker needs. Re-running
# rotates the secret.
#
#   deploy/register-client.sh CLIENT_ID "Display name" https://calbooker.example.com
#
# CLIENT_ID: a-z, 0-9, -. "Display name" is what people see on the provider's
# sign-in page ("Signing in to ..."). The URL is calbooker's public URL. The
# secret goes to stdout only, so redirect it straight into your secret store
# (for example `... > calbooker.creds`; mode 600) and never into Git or logs.
# HYDRA_CONTAINER (default goweb-hydra) and CONTAINER_ENGINE select Hydra.
set -eu

ENGINE="${CONTAINER_ENGINE:-docker}"
HYDRA="${HYDRA_CONTAINER:-goweb-hydra}"
ADMIN_URL="${HYDRA_ADMIN_URL:-http://127.0.0.1:24445}"

[ "$#" -eq 3 ] || { sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
id="$1"
name="$2"
url="${3%/}"
case "$id" in "" | *[!a-z0-9-]*) echo "CLIENT_ID may only use a-z, 0-9, and -" >&2; exit 2 ;; esac
case "$url" in https://*) ;; http://*) [ "${ALLOW_HTTP:-}" = 1 ] || { echo "the app URL must use https (ALLOW_HTTP=1 for a test)" >&2; exit 2; } ;; *) echo "the app URL must be an https:// URL" >&2; exit 2 ;; esac
[ -n "$name" ] || { echo "Display name must not be empty" >&2; exit 2; }

secret="$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')"
hydra() { "$ENGINE" exec "$HYDRA" hydra --endpoint "$ADMIN_URL" "$@"; }

hydra delete oauth2-client "$id" >/dev/null 2>&1 || true
hydra create oauth2-client \
	--id "$id" \
	--secret "$secret" \
	--name "$name" \
	--grant-type authorization_code \
	--response-type code \
	--scope openid,email,profile \
	--token-endpoint-auth-method client_secret_basic \
	--redirect-uri "$url/en/oauth/sso/callback" \
	--post-logout-callback "$url/en/sign-in" \
	--backchannel-logout-callback "$url/oauth/sso/backchannel-logout" \
	--backchannel-logout-session-required \
	--skip-consent \
	--skip-logout-consent \
	--format json >/dev/null

echo "registered $id for $url" >&2
printf 'CALBOOKER_SSO_CLIENT_ID=%s\nCALBOOKER_SSO_CLIENT_SECRET=%s\n' "$id" "$secret"
