#!/usr/bin/env bash
# Deliver locally built images to the deb host over SSH and install them.
# Usage: scripts/deploy-deb.sh <app-tag>
# Requires: images built by scripts/build-image.sh; SSH alias `deb` with
# normal interactive auth (password or agent). Never embeds credentials,
# never disables host key checking.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <app-tag>" >&2
  exit 2
fi
tag="$1"
repo_root="$(cd "$(dirname "$0")/.." && pwd)"

app_image="localhost/home-streaming-assistant:${tag}"

# Dependencies come from the pinned manifest.
alist_ref="$(python3 -c 'import json;print(json.load(open("deploy/images.json"))["alist"]["upstream"])')"
jelly_ref="$(python3 -c 'import json;print(json.load(open("deploy/images.json"))["jellyfin"]["upstream"])')"
alist_local="$(python3 -c 'import json;print(json.load(open("deploy/images.json"))["alist"]["localAlias"])')"
jelly_local="$(python3 -c 'import json;print(json.load(open("deploy/images.json"))["jellyfin"]["localAlias"])')"
podman image exists "$app_image" || { echo "app image missing: $app_image (run scripts/build-image.sh)" >&2; exit 1; }
podman pull "$alist_ref" >/dev/null
podman tag "$alist_ref" "$alist_local"
podman pull "$jelly_ref" >/dev/null
podman tag "$jelly_ref" "$jelly_local"

check_meta() { # image os arch
  local got
  got="$(podman image inspect "$1" --format '{{.Os}} {{.Architecture}}')"
  [[ "$got" == "$2 $3" ]] || { echo "$1 has $got, expected $2/$3" >&2; exit 1; }
}
check_meta "$app_image" linux amd64
check_meta "$alist_local" linux amd64
check_meta "$jelly_local" linux amd64

echo "transferring images to deb..."
for img in "$app_image" "$alist_local" "$jelly_local"; do
  podman save --format docker-archive "$img" | ssh deb 'podman load'
done

# Verify remote image IDs match local (strip optional sha256: prefix).
for img in "$app_image" "$alist_local" "$jelly_local"; do
  local_id="$(podman image inspect "$img" --format '{{.Id}}')"
  remote_id="$(ssh deb "podman image inspect '$img' --format '{{.Id}}'")"
  [[ "${local_id#sha256:}" == "${remote_id#sha256:}" ]] || {
    echo "image ID mismatch for $img: $local_id vs $remote_id" >&2
    exit 1
  }
done

echo "staging deploy files..."
ssh deb 'mkdir -p ~/cloud-media-staging && chmod 700 ~/cloud-media-staging'
scp -q "$repo_root/scripts/install-deb.sh" deb:cloud-media-staging/
scp -q "$repo_root/deploy/config.example.json" deb:cloud-media-staging/deploy/
echo "installing (no credentials in this run; first install provisions them locally on deb)..."
ssh deb "cd ~/cloud-media-staging && bash ./install-deb.sh '$app_image'"

for svc in alist jellyfin home-streaming-assistant; do
  state="$(ssh deb "systemctl --user is-active $svc.service")"
  [[ "$state" == "active" ]] || { echo "$svc.service is $state" >&2; exit 1; }
done
echo "deploy OK: $app_image"
