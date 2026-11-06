#!/usr/bin/env bash
# Build the home-streaming-assistant image locally for linux/amd64.
# Usage: scripts/build-image.sh <tag>
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <tag>" >&2
  exit 2
fi
tag="$1"
if [[ ! "$tag" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo "invalid tag: $tag" >&2
  exit 2
fi

cd "$(dirname "$0")/.."

podman build --platform linux/amd64 -t "localhost/home-streaming-assistant:${tag}" .

info="$(podman image inspect "localhost/home-streaming-assistant:${tag}" \
  --format '{{.Os}} {{.Architecture}} {{index .Config.Labels "org.opencontainers.image.version"}}')"
echo "built: localhost/home-streaming-assistant:${tag} (${info})"
