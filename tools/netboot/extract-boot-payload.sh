#!/usr/bin/env bash
#
# Extract the FAT filesystem of a recovery image (boot.img) into a directory,
# so the SD-provisioning endpoint (POST /api/v1/provisioning/sd) can zip it
# together with a generated node-adoption.txt. The backend reads the directory
# named by PROVISIONING_SD_PAYLOAD_DIR; the community compose points it at
# data/net_install/sd-payload.
#
# Run it after every change of boot.img, from the SAME image the server
# serves: an SD bundle and a network boot then hand a device the same recovery
# system, notices and source offer included (docs/NETWORK-FIRST.md, "Serving
# the recovery image").
#
# Usage:
#   tools/netboot/extract-boot-payload.sh [BOOT_IMG] [OUT_DIR]
#
# Defaults (the community compose layout, relative to the directory you run in):
#   BOOT_IMG = data/net_install/boot.img
#   OUT_DIR  = data/net_install/sd-payload
#
# Requires: mtools (mcopy). No mount and no root needed.
set -euo pipefail

BOOT_IMG="${1:-data/net_install/boot.img}"
OUT_DIR="${2:-data/net_install/sd-payload}"

if ! command -v mcopy >/dev/null 2>&1; then
	echo "error: mtools (mcopy) is not installed" >&2
	exit 1
fi
if [ ! -f "$BOOT_IMG" ]; then
	echo "error: boot image not found: $BOOT_IMG" >&2
	exit 1
fi

# Recreate the output directory from scratch so stale files never leak in.
rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

# Copy the whole FAT filesystem tree out of the image (recursive, no mount).
mcopy -i "$BOOT_IMG" -s "::/*" "$OUT_DIR/"

count=$(find "$OUT_DIR" -type f | wc -l | tr -d ' ')
echo "extracted ${count} files from ${BOOT_IMG} into ${OUT_DIR}"
