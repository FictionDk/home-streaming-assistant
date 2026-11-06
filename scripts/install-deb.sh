#!/usr/bin/env bash
# Install/upgrade home-streaming-assistant on the deb host as user hek.
# Usage (on deb, from the staging dir): bash install-deb.sh <app-image-full-name>
# Requires deploy/quadlet/* and deploy/config.example.json in the CWD.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <app-image-full-name>" >&2
  exit 2
fi
app_image="$1"

base="/srv/cloud-media"
config_dir="$base/app-config"
rclone_dir="$base/rclone"
staging="$(pwd)"

[[ -d deploy/quadlet && -f deploy/config.example.json ]] || {
  echo "run from the staging directory containing deploy/" >&2
  exit 1
}
[[ -d "$base" ]] || { echo "missing $base" >&2; exit 1; }
[[ "$(stat -c '%U' "$base")" == "hek" ]] || { echo "$base not owned by hek" >&2; exit 1; }

mkdir -p "$base/files/.incoming" "$base/files/media" "$base/state" \
         "$config_dir" "$rclone_dir" "$base/alist-data" "$base/alist-logs" \
         "$base/jellyfin-config" "$base/jellyfin-cache"
chmod 700 "$base"
chmod 700 "$config_dir" "$rclone_dir" 2>/dev/null || podman unshare chmod 700 "$config_dir" "$rclone_dir"

install_secret() { # <staging-file> <target-relative-path>
  podman unshare install -m 0600 -o 10001 -g 10001 "$staging/$1" "$base/$2"
  rm -f "$staging/$1"
}


first_install=0
# app-config is owned by the namespace UID, so plain hek cannot stat
# inside it; every existence check and install runs through podman unshare.
if ! podman unshare test -f "$config_dir/config.json"; then
  first_install=1
  cp deploy/config.example.json "$staging/config.json"
  install_secret "config.json" "app-config/config.json"
fi
if ! podman unshare test -f "$config_dir/admin-password"; then
  first_install=1
  umask 077
  openssl rand -hex 32 > "$staging/admin-password"
  install_secret "admin-password" "app-config/admin-password"
fi
for f in alist-password jellyfin-api-key; do
  if [[ -f "$staging/$f" ]]; then
    install_secret "$f" "app-config/$f"
  fi
done
if [[ -f "$staging/rclone.conf" ]]; then
  install_secret "rclone.conf" "rclone/rclone.conf"
fi

# Quadlet units (never overwrite an existing Jellyfin unit that may have
# been republished; upgrades keep it).
mkdir -p "$HOME/.config/containers/systemd"
install -m 600 deploy/quadlet/cloud-media.network "$HOME/.config/containers/systemd/"
[[ -f "$HOME/.config/containers/systemd/alist.container" ]] || \
  install -m 600 deploy/quadlet/alist.container "$HOME/.config/containers/systemd/"
[[ -f "$HOME/.config/containers/systemd/jellyfin.container" ]] || \
  install -m 600 deploy/quadlet/jellyfin.container "$HOME/.config/containers/systemd/"
cat > "$HOME/.config/containers/systemd/home-streaming-assistant.container" <<EOF
$(sed "s|@APP_IMAGE@|$app_image|" deploy/quadlet/home-streaming-assistant.container.in)
EOF
chmod 600 "$HOME/.config/containers/systemd/home-streaming-assistant.container"
# App data dirs writable by the namespace user.
podman unshare chown -R 10001:10001 "$base/state" "$base/files/.incoming" "$base/files/media"
podman unshare chmod 700 "$base/files/.incoming"
podman unshare chmod 755 "$base/files/media"

systemctl --user daemon-reload
# Quadlet units are generated; [Install] WantedBy=default.target handles
# boot start under linger, so only start them here.
systemctl --user start alist.service jellyfin.service home-streaming-assistant.service

if [[ $first_install -eq 1 ]]; then
  echo "first install: config and admin password provisioned"
fi
echo "install complete: $app_image"
