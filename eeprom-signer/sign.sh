#!/usr/bin/env bash
#
# Build ONE per-device signed Raspberry Pi EEPROM image with the device's real
# serial baked into HTTP_PATH, so the platform can serve/withhold that device's
# boot.img by serial (see docs/NETWORK-FIRST.md, Variant B). Runs inside the
# isolated eeprom-signer container which is the only place that holds the private
# signing key.
#
# Output (into $OUTDIR): recovery.bin, pieeprom.upd, pieeprom.sig, SHA256SUMS
#
# Inputs via env:
#   SOC          cm4|cm5                              (required)
#   SERIAL       device hardware serial               (required; baked into HTTP_PATH)
#   HTTP_HOST    netboot host                         (required, e.g. 192.0.2.10)
#   ADOPTION_B64 base64 master-token adoption blob    (required; -> UPTIME_TOKEN)
#   BOOT_ORDER   hex boot order                       (default 0xf1467 = http,nvme,usb,sd,restart)
#   HTTP_PATH_PREFIX  path prefix before the serial   (default http-boot)
#   OUTDIR       output directory                     (required)
#   EEPROM_DIR   base with rpi-eeprom clone + tools   (default /eeprom-build)
#   PRIVKEY / PUBKEY  signing keys                    (default /keys/{privkey,pubkey}.pem)
set -euo pipefail

: "${SOC:?SOC required (cm4|cm5)}"
: "${SERIAL:?SERIAL required}"
: "${HTTP_HOST:?HTTP_HOST required}"
: "${ADOPTION_B64:?ADOPTION_B64 required}"
: "${OUTDIR:?OUTDIR required}"
BOOT_ORDER="${BOOT_ORDER:-0xf1467}"
HTTP_PATH_PREFIX="${HTTP_PATH_PREFIX:-http-boot}"

EEPROM_DIR="${EEPROM_DIR:-/eeprom-build}"
RPI_EEPROM="${RPI_EEPROM:-$EEPROM_DIR/rpi-eeprom}"
CFG="${RPI_EEPROM_CONFIG:-$EEPROM_DIR/tools/rpi-eeprom-config}"
DIG="${RPI_EEPROM_DIGEST:-$EEPROM_DIR/tools/rpi-eeprom-digest}"
PRIVKEY="${PRIVKEY:-/keys/privkey.pem}"
PUBKEY="${PUBKEY:-/keys/pubkey.pem}"

# Validate every input that ends up in a filesystem path or the EEPROM config so
# a caller can't inject anything. These are FULL-STRING anchored: a glob that
# anchors only the prefix (`0x[0-9A-Fa-f]*`) lets a tail like
# `0x1|;s|^HTTP_HOST=.*|HTTP_HOST=evil` through. server.py validates the same
# values first; this is defence in depth for any other caller of the script.
_boot_order_re='^0x[0-9A-Fa-f]{1,8}$'
_http_host_re='^[A-Za-z0-9.-]{1,253}$'
# ADOPTION_B64 becomes the UPTIME_TOKEN line: padded base64 only (a line break
# would add configuration lines under the signature), at most 1536 characters.
_adoption_re='^([A-Za-z0-9+/]{4})*([A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$'
if [[ ! "$ADOPTION_B64" =~ $_adoption_re ]] || [ "${#ADOPTION_B64}" -gt 1536 ] || [[ "$ADOPTION_B64" == *$'\n'* ]]; then
	echo "invalid ADOPTION_B64" >&2; exit 2
fi
case "$SERIAL" in
	*[!A-Za-z0-9._-]*|"") echo "invalid SERIAL" >&2; exit 2 ;;
esac
if [[ ! "$BOOT_ORDER" =~ $_boot_order_re ]]; then
	echo "invalid BOOT_ORDER" >&2; exit 2
fi
# hostname or IPv4 literal, no leading/trailing dot.
if [[ ! "$HTTP_HOST" =~ $_http_host_re ]] || [[ "$HTTP_HOST" == .* ]] || [[ "$HTTP_HOST" == *. ]]; then
	echo "invalid HTTP_HOST" >&2; exit 2
fi

case "$SOC" in
	cm4) FW="$RPI_EEPROM/firmware-2711/latest" ;;
	cm5) FW="$RPI_EEPROM/firmware-2712/latest" ;;
	*) echo "unknown SOC '$SOC'" >&2; exit 2 ;;
esac

BASE=$(ls -1 "$FW"/pieeprom-*.bin 2>/dev/null | sort | tail -1 || true)
[ -n "$BASE" ] && [ -f "$BASE" ] || { echo "no pieeprom base in $FW" >&2; exit 1; }
[ -f "$FW/recovery.bin" ] || { echo "no recovery.bin in $FW" >&2; exit 1; }
[ -f "$PRIVKEY" ] || { echo "private key not found: $PRIVKEY" >&2; exit 1; }
[ -f "$PUBKEY" ] || { echo "public key not found: $PUBKEY" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Start from the firmware base config and set the network-first + per-device vars.
python3 "$CFG" "$BASE" > "$work/boot.conf"
set_var() {
	# The value is passed through the ENVIRONMENT into a python3 rewrite, never
	# interpolated into a program. A `sed -i "s|^${key}=.*|...|"` would make the
	# value part of the sed script, so a `|` in it could rewrite other lines and
	# GNU sed's `e` flag could execute a command. Here the value is only ever
	# data: python replaces every `KEY=` line, or appends `KEY=value`.
	SV_KEY="$1" SV_VAL="$2" SV_FILE="$work/boot.conf" python3 - <<'PY'
import os

key = os.environ["SV_KEY"]
val = os.environ["SV_VAL"]
path = os.environ["SV_FILE"]
prefix = key + "="

with open(path) as fh:
    lines = fh.read().splitlines()

out = []
replaced = False
for line in lines:
    # Rewrite every `KEY=` line, not only the first.
    if line.startswith(prefix):
        out.append(prefix + val)
        replaced = True
    else:
        out.append(line)
if not replaced:
    out.append(prefix + val)

with open(path, "w") as fh:
    fh.write("\n".join(out) + "\n")
PY
}
set_var BOOT_ORDER "$BOOT_ORDER"
set_var PCIE_PROBE 1
set_var NET_INSTALL_AT_POWER_ON 1
set_var HTTP_HOST "$HTTP_HOST"
set_var HTTP_PATH "${HTTP_PATH_PREFIX}/${SERIAL}"
set_var UPTIME_TOKEN "$ADOPTION_B64"
# NOTE: we embed our pubkey (--pubkey below) but deliberately do NOT set
# SIGNED_BOOT. Hardware-proven model (see docs/NETWORK-FIRST.md): network boot
# always verifies boot.sig, so with our pubkey embedded it verifies our signed
# bootstrap; but because SIGNED_BOOT is unset, local boot is NOT enforced, so a
# stock OS on NVMe (no boot.sig) still boots via loose files. Enabling SIGNED_BOOT
# would break the local OS boot (Error 6, needs a signed boot.img).

# Sign the config, then bake config + our pubkey into a fresh pieeprom image.
sh "$DIG" -i "$work/boot.conf" -o "$work/boot.conf.sig" -k "$PRIVKEY"

# $OUTDIR is served while this runs, so the package is built in a staging
# directory inside it and renamed into place file by file, manifest last: a
# reader never sees a half-written file, and a build that fails part-way leaves
# the previous package whole. Same filesystem, so each rename is atomic; the
# dot name is never served. Consistency ACROSS a reader's separate requests is
# the caller's to keep: the platform does not rebuild a node's package while a
# flash it dispatched may still be fetching it, and server.py runs one build
# per directory at a time.
mkdir -p "$OUTDIR"
stage=$(mktemp -d "$OUTDIR/.stage.XXXXXX")
trap 'rm -rf "$work" "$stage"' EXIT
python3 "$CFG" --config "$work/boot.conf" --digest "$work/boot.conf.sig" --pubkey "$PUBKEY" \
	--out "$stage/pieeprom.upd" "$BASE"
sh "$DIG" -i "$stage/pieeprom.upd" -o "$stage/pieeprom.sig" -k "$PRIVKEY"
cp "$FW/recovery.bin" "$stage/recovery.bin"
( cd "$stage" && sha256sum recovery.bin pieeprom.upd pieeprom.sig > SHA256SUMS )
for f in recovery.bin pieeprom.upd pieeprom.sig SHA256SUMS; do
	mv -f "$stage/$f" "$OUTDIR/$f"
done

echo "signed EEPROM for serial=$SERIAL soc=$SOC -> $OUTDIR"
python3 "$CFG" "$OUTDIR/pieeprom.upd" | grep -E '^BOOT_ORDER=|^HTTP_HOST=|^HTTP_PATH=|^SIGNED_BOOT=|^PCIE_PROBE=' || true
