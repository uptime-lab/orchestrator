#!/usr/bin/env bash
#
# Sign a recovery image with THIS install's EEPROM key and put it in place:
# boot.img -> boot.sig, verified, then both moved into the net_install
# directory the backend serves at /http-boot/<serial>/.
#
# boot.sig is not a raw signature. The Raspberry Pi boot ROM reads a
# three-line text file, produced by rpi-eeprom-digest:
#
#   <sha256 hex of boot.img>
#   ts: <unix time>
#   rsa2048: <hex RSA-2048 signature over the image bytes>
#
# `openssl dgst -sign … -out boot.sig` computes the right signature and writes
# it in the wrong shape, and the ROM rejects it on every device. This script
# therefore checks the shape and verifies the signature against the public key
# before anything is replaced.
#
# The image is signed as a COPY beside the live one and both files are then
# renamed into place, so the window in which boot.img and boot.sig disagree is
# two renames long instead of the duration of the signing. The previous pair
# is kept as boot.img.prev / boot.sig.prev, so a rollback is two `mv`s.
#
# Usage:
#   tools/netboot/sign-boot-image.sh NEW_BOOT_IMG [NET_INSTALL_DIR]
#
# Environment (defaults: the community compose layout, relative to the
# directory you run in):
#   NET_INSTALL_DIR  data/net_install           (also the second argument)
#   KEY_DIR          data/eeprom-keys           privkey.pem + pubkey.pem
#   EEPROM_BUILD     data/eeprom-build          tools/rpi-eeprom-digest
#                                               (tools/netboot/update-eeprom-firmware.sh)
#
# The private key must be the one whose public half the devices' bootloaders
# embed — the same key the eeprom-signer mounts. A different key yields a
# well-formed boot.sig that every device rejects.
#
# Requires: bash, openssl, xxd, sha256sum.
set -euo pipefail

NEW="${1:?usage: sign-boot-image.sh NEW_BOOT_IMG [NET_INSTALL_DIR]}"
NI="${2:-${NET_INSTALL_DIR:-data/net_install}}"
KEY_DIR="${KEY_DIR:-data/eeprom-keys}"
EEPROM_BUILD="${EEPROM_BUILD:-data/eeprom-build}"
PRIVKEY="$KEY_DIR/privkey.pem"
PUBKEY="$KEY_DIR/pubkey.pem"
DIG="$EEPROM_BUILD/tools/rpi-eeprom-digest"

for t in openssl xxd sha256sum; do
	command -v "$t" >/dev/null 2>&1 || { echo "error: $t is not installed" >&2; exit 1; }
done
[ -f "$NEW" ] || { echo "error: image not found: $NEW" >&2; exit 1; }
[ -d "$NI" ] || { echo "error: net_install directory not found: $NI" >&2; exit 1; }
[ -f "$PRIVKEY" ] || { echo "error: private key not found: $PRIVKEY" >&2; exit 1; }
[ -f "$PUBKEY" ] || { echo "error: public key not found: $PUBKEY" >&2; exit 1; }
[ -f "$DIG" ] || { echo "error: $DIG not found; run tools/netboot/update-eeprom-firmware.sh first" >&2; exit 1; }

img_tmp="$NI/.boot.img.new"
sig_tmp="$NI/.boot.sig.new"
work=$(mktemp -d)
trap 'rm -rf "$work" "$img_tmp" "$sig_tmp"' EXIT

# The copy lives in the target directory, so the final rename stays on one
# filesystem and is atomic per file.
cp "$NEW" "$img_tmp"
sh "$DIG" -i "$img_tmp" -o "$sig_tmp" -k "$PRIVKEY"

# Shape: three lines, the first the image's sha256.
lines=$(wc -l < "$sig_tmp" | tr -d ' ')
want=$(sha256sum "$img_tmp" | cut -d' ' -f1)
got=$(head -1 "$sig_tmp")
[ "$lines" = 3 ] || { echo "error: boot.sig has $lines lines, expected 3" >&2; exit 1; }
[ "$got" = "$want" ] || { echo "error: boot.sig line 1 is not the image's sha256" >&2; exit 1; }
grep -q '^ts: [0-9]\+$' "$sig_tmp" || { echo "error: boot.sig has no ts: line" >&2; exit 1; }

# Signature: verify it against the public key the devices embed.
sed -n 's/^rsa2048: //p' "$sig_tmp" | xxd -r -p > "$work/sig.bin"
openssl dgst -sha256 -verify "$PUBKEY" -signature "$work/sig.bin" "$img_tmp" >/dev/null \
	|| { echo "error: the signature does not verify against $PUBKEY" >&2; exit 1; }

# Keep the previous pair, then swap.
[ -f "$NI/boot.img" ] && cp -p "$NI/boot.img" "$NI/boot.img.prev"
[ -f "$NI/boot.sig" ] && cp -p "$NI/boot.sig" "$NI/boot.sig.prev"
chmod 0644 "$img_tmp" "$sig_tmp"
mv "$img_tmp" "$NI/boot.img"
mv "$sig_tmp" "$NI/boot.sig"

echo "signed and installed $NI/boot.img (sha256 $want)"
echo "next: tools/netboot/extract-boot-payload.sh $NI/boot.img $NI/sd-payload"
