#!/bin/sh
# Runs calbooker as one container on the host network, next to the Hydra and
# provider containers from the goweb deployment.
#
#   deploy/run-calbooker.sh start ENV_FILE
#   deploy/run-calbooker.sh stop
#
# Start from deploy/env/calbooker.env.example and register the app in Hydra
# with deploy/register-client.sh first. Image: CALBOOKER_IMAGE (pin a tag).
# Container name: CALBOOKER_CONTAINER (default calbooker).
set -eu

ENGINE="${CONTAINER_ENGINE:-docker}"
IMAGE="${CALBOOKER_IMAGE:-ghcr.io/tewecske/calbooker:latest}"
NAME="${CALBOOKER_CONTAINER:-calbooker}"

case "${1:-}" in
start)
	[ "$#" -eq 2 ] || { echo "usage: $0 start ENV_FILE" >&2; exit 2; }
	env_file="$2"
	[ -f "$env_file" ] || { echo "ENV_FILE not found: $env_file" >&2; exit 2; }
	if [ -n "$(find "$env_file" -perm /077 2>/dev/null)" ]; then
		echo "warning: $env_file is readable by other users; chmod 600 it" >&2
	fi
	"$ENGINE" rm -f "$NAME" >/dev/null 2>&1 || true
	# The app writes nothing to disk, needs no capabilities, and logs to stdout.
	"$ENGINE" run -d --name "$NAME" --restart unless-stopped --network host \
		--read-only --cap-drop ALL --security-opt no-new-privileges \
		--env-file "$env_file" "$IMAGE"
	;;
stop)
	"$ENGINE" rm -f "$NAME"
	;;
*) echo "usage: $0 start ENV_FILE | stop" >&2; exit 2 ;;
esac
