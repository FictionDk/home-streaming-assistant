#!/usr/bin/env bash
# After the Jellyfin startup wizard completes and StartupWizardConfirmed,
# replace the loopback-only PublishPort with the LAN/Tailscale addresses.
# Run on deb as hek:  bash jellyfin-publish.sh
set -euo pipefail

unit="$HOME/.config/containers/systemd/jellyfin.container"
[[ -f "$unit" ]] || { echo "missing $unit" >&2; exit 1; }

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
sed -e 's|^PublishPort=.*|PublishPort=192.168.200.175:8096:8096\nPublishPort=100.75.124.126:8096:8096|' \
    -e 's|^# First bootstrap binds loopback only.*||' \
    -e 's|^# wizard completion opens the LAN/Tailscale addresses.||' \
    "$unit" > "$tmp"
mv "$tmp" "$unit"
chmod 600 "$unit"

systemctl --user daemon-reload
systemctl --user restart jellyfin.service
echo "jellyfin republished on 192.168.200.175:8096 and 100.75.124.126:8096"
