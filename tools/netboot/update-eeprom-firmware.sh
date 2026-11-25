#!/usr/bin/env bash
#
# Populate, or bring up to date, the EEPROM build directory the eeprom-signer
# reads (mounted read-only at /eeprom-build). eeprom-signer/sign.sh expects
# exactly this layout:
#
#   <dir>/rpi-eeprom/                 a clone of the Raspberry Pi rpi-eeprom
#                                     repository; the signer bakes from
#                                     firmware-2711/latest (CM4, Pi 4) and
#                                     firmware-2712/latest (CM5, Pi 5)
#   <dir>/tools/rpi-eeprom-config     copied from the clone
#   <dir>/tools/rpi-eeprom-digest     copied from the clone
#
# A bare clone at <dir> itself is NOT that layout; the signer then answers
# "no pieeprom base".
#
# The first run clones; every later run fast-forwards the clone (a local
# change makes it fail loudly rather than merge) and refreshes the two tools.
# Run it periodically — a stale clone makes the UI compare devices against an
# old firmware base, so no bootloader update is ever offered, with no error.
# It never flashes a device; flashing stays an explicit action in the UI.
#
# Usage:
#   tools/netboot/update-eeprom-firmware.sh [EEPROM_BUILD_DIR]
#
# Default: data/eeprom-build (the community compose layout, relative to the
# directory you run in). Run it as the user that owns the directory; the
# signer only needs to READ it.
#
# Requires: git.
set -euo pipefail

DIR="${1:-data/eeprom-build}"
REPO_URL="${RPI_EEPROM_REPO:-https://github.com/raspberrypi/rpi-eeprom.git}"
CLONE="$DIR/rpi-eeprom"

mkdir -p "$DIR/tools"

if [ ! -d "$CLONE/.git" ]; then
	echo "cloning $REPO_URL into $CLONE"
	git clone -q --depth 1 "$REPO_URL" "$CLONE"
	before=""
else
	before=$(git -C "$CLONE" rev-parse HEAD)
	git -C "$CLONE" fetch -q origin
	git -C "$CLONE" merge -q --ff-only "origin/$(git -C "$CLONE" rev-parse --abbrev-ref HEAD)"
fi
after=$(git -C "$CLONE" rev-parse HEAD)

for tool in rpi-eeprom-config rpi-eeprom-digest; do
	[ -f "$CLONE/$tool" ] || { echo "error: $tool not found in $CLONE" >&2; exit 1; }
	install -m 0755 "$CLONE/$tool" "$DIR/tools/$tool"
done

# `|| true`: under pipefail a missing directory fails the pipeline, and the
# assignment below would then end the script without the error that names it.
latest() { { ls -1 "$CLONE/firmware-$1/latest"/pieeprom-*.bin 2>/dev/null || true; } | sort | tail -1 | xargs -r basename; }
cm4=$(latest 2711)
cm5=$(latest 2712)
[ -n "$cm4" ] && [ -n "$cm5" ] || { echo "error: no pieeprom base under $CLONE/firmware-27xx/latest" >&2; exit 1; }

msg="$(date -u +%FT%TZ) HEAD=${after:0:8} 2711=${cm4} 2712=${cm5}"
if [ -n "$before" ] && [ "$before" != "$after" ]; then msg="$msg (updated from ${before:0:8})"; fi
echo "$msg"
