#!/usr/bin/env python3
"""Minimal internal HTTP API for the eeprom-signer container.

The only place that holds the private signing key. The internet-facing backend
calls POST /sign to produce a per-device signed EEPROM package (Variant B, see
docs/NETWORK-FIRST.md); the package is written under
OUTPUT_ROOT/by-serial/<serial>/<node_id>/ (by-serial/<serial>/ for a caller that
names no node), which the backend serves to the holder of that node's download
capability for the agent to download and flash.

Stdlib only. Not exposed publicly — bound on a signer-only docker network shared
with the backend alone. SIGNER_TOKEN (Authorization: Bearer ...) is REQUIRED, not
optional: main() exits 1 without one.
"""
import json
import os
import re
import hmac
import shutil
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

OUTPUT_ROOT = os.environ.get("OUTPUT_ROOT", "/output")
SIGN_SCRIPT = os.environ.get("SIGN_SCRIPT", "/app/sign.sh")
# SIGNER_TOKEN is REQUIRED. This process holds the private EEPROM signing key,
# and "internal network" is not an authorization boundary: every container on
# the shared bridge (traefik, frontend nginx) could reach :9099 and mint signed
# packages. Refusing to start without a token makes a missing configuration a
# loud failure instead of a silently open signing oracle.

def _secret_env(name):
    """Read a credential from a file when one is named, else from the environment.

    SIGNER_TOKEN_FILE wins over SIGNER_TOKEN, the Docker-secrets convention the
    backend's loaders follow: a container's whole environment is exposed by
    `docker inspect` and by /proc/<pid>/environ, so a credential in the
    environment is readable by anything that can reach the Docker API.

    A named-but-unreadable file yields empty and does NOT fall back to the
    environment. Here that is the loudest possible failure and the right one:
    this process refuses to start without a token, so a rotation that did not
    take effect stops the signer instead of leaving it running on the old value.
    """
    path = os.environ.get(name + "_FILE", "").strip()
    if not path:
        return os.environ.get(name, "")
    try:
        with open(path, "r") as handle:
            return handle.read().rstrip("\r\n")
    except OSError as err:
        print(f"failed to read {name} from {path}: {err}", file=sys.stderr)
        return ""


SIGNER_TOKEN = _secret_env("SIGNER_TOKEN")
HTTP_HOST_DEFAULT = os.environ.get("HTTP_HOST", "")
# The canonical network-first order (backend domain/node.NetworkFirstBootOrder
# renders the same value); reached only when neither the env nor the request
# names one, but a fallback that disagrees with every explicit call is a trap.
BOOT_ORDER_DEFAULT = os.environ.get("BOOT_ORDER", "0xf1467")

# The domain's own serial shape, the same one the httpboot handler enforces: a
# Pi serial is 8-16 hex chars, and nothing else may name a directory under
# by-serial/ — a looser charset would accept "..", which is a path, not a serial.
SERIAL_RE = re.compile(r"^[0-9a-fA-F]{8,16}$")
SOC_RE = re.compile(r"^(cm4|cm5)$")
# A node id names a directory under by-serial/<serial>/, so only the canonical
# lowercase UUID spelling the backend sends is accepted.
NODE_ID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
# http_host and boot_order both end up in the EEPROM config that sign.sh writes,
# so they are validated here as tightly as serial/soc: a value that reached a
# config rewrite unvalidated could alter other config lines, and the rewrite
# runs beside the private key. boot_order is `0x` + 1-8 hex (the backend
# launders it through domain/node.BootOrderToHex); http_host is a hostname or
# IPv4 literal, no leading/trailing dot. sign.sh guards the same two values
# again, defence in depth.
HTTP_HOST_RE = re.compile(r"^[A-Za-z0-9.-]{1,253}$")
BOOT_ORDER_RE = re.compile(r"^0x[0-9a-fA-F]{1,8}$")
# adoption_b64 becomes the UPTIME_TOKEN line of the signed configuration, so a
# line break in it would add configuration lines (a SIGNED_BOOT, a second
# HTTP_HOST) under the platform's signature. It is standard padded base64 and
# nothing else, and the bootloader configuration has only a few kilobytes in
# all: 1536 characters is twice what the backend's blob reaches with
# maximum-length host names. sign.sh enforces the same shape.
ADOPTION_B64_RE = re.compile(r"\A(?:[A-Za-z0-9+/]{4}){1,384}\Z|\A(?:[A-Za-z0-9+/]{4}){0,383}[A-Za-z0-9+/]{2}(?:==|[A-Za-z0-9+/]=)\Z")

EEPROM_DIR = os.environ.get("EEPROM_DIR", "/eeprom-build")

# Every rpi-eeprom image embeds its identity as plain strings.
IMG_VERSION_RE = re.compile(rb"VERSION:([0-9a-fA-F]{8,40})")
IMG_BUILD_TS_RE = re.compile(rb"BUILD_TIMESTAMP=(\d{1,19})")


def base_firmware():
    """Identity of the newest bootloader image per SoC — what /sign would bake
    from right now. Lets the platform tell devices whether they are up to date."""
    out = {}
    for soc, fwdir in (("cm4", "firmware-2711"), ("cm5", "firmware-2712")):
        d = os.path.join(EEPROM_DIR, "rpi-eeprom", fwdir, "latest")
        try:
            imgs = sorted(f for f in os.listdir(d) if f.startswith("pieeprom-") and f.endswith(".bin"))
        except OSError:
            continue
        if not imgs:
            continue
        try:
            with open(os.path.join(d, imgs[-1]), "rb") as f:
                data = f.read()
        except OSError:
            continue
        v = IMG_VERSION_RE.search(data)
        if not v:
            continue
        entry = {"version": v.group(1).decode().lower(), "image": imgs[-1]}
        ts = IMG_BUILD_TS_RE.search(data)
        if ts:
            entry["build_timestamp"] = int(ts.group(1))
        out[soc] = entry
    return out


def bootcode_path(soc):
    """Path to the GENERIC (non-per-device) second-stage recovery.bin for a SoC.
    This is the bytes the RPIBOOT ROM loads as bootcode4.bin (cm4)/bootcode5.bin
    (cm5); a browser WebUSB flasher pushes it to harvest the board serial before
    baking a per-serial image."""
    fwdir = {"cm4": "firmware-2711", "cm5": "firmware-2712"}.get(soc)
    if not fwdir:
        return None
    return os.path.join(EEPROM_DIR, "rpi-eeprom", fwdir, "latest", "recovery.bin")


class Handler(BaseHTTPRequestHandler):
    def _send(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            return self._send(200, {"ok": True})
        if self.path == "/base":
            return self._send(200, base_firmware())
        if self.path.startswith("/bootcode"):
            return self._serve_bootcode()
        return self._send(404, {"error": "not found"})

    def _serve_bootcode(self):
        from urllib.parse import urlparse, parse_qs
        soc = (parse_qs(urlparse(self.path).query).get("soc") or [""])[0]
        if not SOC_RE.match(soc):
            return self._send(400, {"error": "invalid soc (cm4|cm5)"})
        p = bootcode_path(soc)
        try:
            with open(p, "rb") as f:
                data = f.read()
        except OSError:
            return self._send(404, {"error": "bootcode not found for soc"})
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        if self.path != "/sign":
            return self._send(404, {"error": "not found"})
        # Constant-time compare so the token cannot be recovered a byte at a
        # time by timing the response.
        presented = self.headers.get("Authorization") or ""
        if not hmac.compare_digest(presented, f"Bearer {SIGNER_TOKEN}"):
            return self._send(401, {"error": "unauthorized"})

        try:
            length = int(self.headers.get("Content-Length", 0))
            req = json.loads(self.rfile.read(length) or b"{}")
        except (ValueError, TypeError):
            return self._send(400, {"error": "invalid json"})

        soc = str(req.get("soc", ""))
        serial = str(req.get("serial", ""))
        http_host = str(req.get("http_host", HTTP_HOST_DEFAULT))
        adoption_b64 = str(req.get("adoption_b64", ""))
        boot_order = str(req.get("boot_order", BOOT_ORDER_DEFAULT))

        if not SOC_RE.match(soc):
            return self._send(400, {"error": "invalid soc (cm4|cm5)"})
        if not SERIAL_RE.match(serial):
            return self._send(400, {"error": "invalid serial"})
        if not http_host:
            return self._send(400, {"error": "http_host required"})
        if (not HTTP_HOST_RE.match(http_host)
                or http_host.startswith(".") or http_host.endswith(".")):
            return self._send(400, {"error": "invalid http_host"})
        if not BOOT_ORDER_RE.match(boot_order):
            return self._send(400, {"error": "invalid boot_order"})
        if not adoption_b64:
            return self._send(400, {"error": "adoption_b64 required"})
        if not ADOPTION_B64_RE.match(adoption_b64):
            return self._send(400, {"error": "invalid adoption_b64 (padded base64, at most 1536 characters)"})
        node_id = str(req.get("node_id", ""))
        if node_id and not NODE_ID_RE.match(node_id):
            return self._send(400, {"error": "invalid node_id"})

        # A package carries ONE node's adoption token, so it is written into
        # that node's own directory when the caller names it. A short serial is
        # shared by unrelated devices; keyed on the serial alone, a second
        # account's bake would overwrite the first, and the first account's
        # download capability would then open the second account's credential. The backend's
        # download handler serves only by-serial/<serial>/<node_id>/.
        rel = f"eeprom/by-serial/{serial}"
        outdir = os.path.join(OUTPUT_ROOT, "by-serial", serial)
        if node_id:
            rel = f"{rel}/{node_id}"
            outdir = os.path.join(outdir, node_id)
        env = dict(os.environ)
        env.update({
            "SOC": soc, "SERIAL": serial, "HTTP_HOST": http_host,
            "ADOPTION_B64": adoption_b64, "BOOT_ORDER": boot_order, "OUTDIR": outdir,
        })
        # One build per package directory at a time: sign.sh publishes by
        # renaming its files into place, and two builds interleaving those
        # renames would leave one build's manifest beside the other's image.
        try:
            with _outdir_lock(outdir):
                _remove_stale_stages(outdir)
                out = subprocess.run(
                    ["bash", SIGN_SCRIPT], env=env, capture_output=True, text=True, timeout=120,
                )
        except subprocess.TimeoutExpired:
            return self._send(500, {"error": "sign timeout"})
        if out.returncode != 0:
            return self._send(500, {"error": "sign failed", "stderr": out.stderr[-2000:]})

        return self._send(200, {
            "ok": True,
            "serial": serial,
            "soc": soc,
            "boot_order": boot_order,
            # Path relative to the boot-serve root the agent downloads from.
            "path": rel,
            "files": ["recovery.bin", "pieeprom.upd", "pieeprom.sig", "SHA256SUMS"],
        })

    def do_DELETE(self):
        """Remove a departed tenant's signed EEPROM package.

        Exists because purge tooling that does not own this tree cannot delete
        from it: the tree is uid-1000 and signer-owned, so a process running as
        another uid (for example the backend-storage owner, uid 100) gets
        permission denied on a direct rw mount, and every package deletion of a
        purge fails (reported as an EEPROM failure, not as a permission error).
        A shared supplementary group does not fix it either: packages this
        process creates later are 0755.

        So deletion is delegated to the one process that already owns the tree rw
        and already gates writes by SIGNER_TOKEN. Three routes:

        DELETE /by-node/<node_id> removes that node's package under EVERY serial
        directory (_delete_by_node) — what the purge uses, because the serial a
        package was baked under is not necessarily the one the node carries now.

        DELETE /by-serial/<serial>/<node_id> removes ONE node's package. A node id
        is unique, so that directory belongs to exactly the node the caller names;
        it is idempotent (absent is 200, deleted:false).

        DELETE /by-serial/<serial> removes only the LEGACY remainder — files
        written directly under the serial directory before packages were keyed on
        the node — and the directory itself only if it is then empty. It never
        removes a node subdirectory: a short serial is shared by unrelated
        devices, so the serial directory can hold another tenant's live package
        beside the one being purged. When node directories remain the answer is
        409 with their count (the legacy files are gone by then), so a caller
        that still believes "one serial = one tenant" cannot take another
        tenant's package through this route.

        The caller is responsible for never asking for the legacy remainder of a
        serial a live node still owns — the purge keeps that cross-tenant guard,
        where the database can answer it. This endpoint enforces only what it can
        see: the shapes, and that every path it builds stays inside by-serial/.
        """
        prefix = "/by-serial/"
        if self.path.startswith("/by-node/"):
            return self._delete_by_node()
        if not self.path.startswith(prefix):
            return self._send(404, {"error": "not found"})

        presented = self.headers.get("Authorization") or ""
        if not hmac.compare_digest(presented, f"Bearer {SIGNER_TOKEN}"):
            return self._send(401, {"error": "unauthorized"})

        from urllib.parse import unquote, urlparse
        # Split BEFORE unquoting, so an encoded "/" inside a segment cannot add
        # a segment; each segment is then held to its own shape.
        raw = urlparse(self.path).path[len(prefix):]
        segments = [unquote(s) for s in raw.split("/")]
        if len(segments) not in (1, 2):
            return self._send(404, {"error": "not found"})
        serial = segments[0]
        # The same shapes /sign validates. They already exclude "/" and "..", so
        # a traversal cannot be spelled; the realpath checks below are the belt
        # to those braces, because a symlink inside the tree would not be
        # visible to a regex at all.
        if not SERIAL_RE.match(serial):
            return self._send(400, {"error": "invalid serial"})
        node_id = segments[1] if len(segments) == 2 else ""
        if len(segments) == 2 and not NODE_ID_RE.match(node_id):
            return self._send(400, {"error": "invalid node_id"})

        root = os.path.realpath(os.path.join(OUTPUT_ROOT, "by-serial"))
        serial_dir = _contained(root, os.path.join(root, serial))
        if serial_dir is None:
            return self._send(400, {"error": "invalid serial"})

        if node_id:
            return self._delete_node_package(serial_dir, serial, node_id)
        return self._delete_legacy_remainder(serial_dir, serial)

    def _delete_by_node(self):
        """DELETE /by-node/<node_id>: remove by-serial/*/<node_id>/ under EVERY serial.

        The purge cannot name the serial a package was baked under: a WebUSB
        flash bakes under the SHORT serial the browser read, and the node is
        rebound to its FULL serial on first connect, so the serial the database
        holds at purge time is not the directory's name. A node id is unique, so
        every serial directory's <node_id> subdirectory is that node's and
        nobody else's. Idempotent: nothing found is 200 with deleted:false and
        an empty list, which the caller reports as ABSENT, never as deleted.
        """
        presented = self.headers.get("Authorization") or ""
        if not hmac.compare_digest(presented, f"Bearer {SIGNER_TOKEN}"):
            return self._send(401, {"error": "unauthorized"})

        from urllib.parse import unquote, urlparse
        raw = urlparse(self.path).path[len("/by-node/"):]
        segments = [unquote(s) for s in raw.split("/")]
        if len(segments) != 1:
            return self._send(404, {"error": "not found"})
        node_id = segments[0]
        if not NODE_ID_RE.match(node_id):
            return self._send(400, {"error": "invalid node_id"})

        root = os.path.realpath(os.path.join(OUTPUT_ROOT, "by-serial"))
        try:
            with os.scandir(root) as serials:
                names = sorted(e.name for e in serials)
        except FileNotFoundError:
            names = []
        except OSError as err:
            return self._send(500, {"error": "delete failed", "detail": str(err)})
        removed = []
        for name in names:
            # Only directories shaped like a serial; anything else under
            # by-serial/ is not this route's to walk.
            if not SERIAL_RE.match(name):
                continue
            serial_dir = _contained(root, os.path.join(root, name))
            if serial_dir is None or not os.path.isdir(serial_dir):
                continue
            candidate = os.path.join(serial_dir, node_id)
            # A symlink named like the node is never followed or removed here:
            # it is not a package this signer wrote, and realpath would resolve
            # it somewhere the containment check exists to refuse.
            if os.path.islink(candidate) or not os.path.isdir(candidate):
                continue
            target = _contained(serial_dir, candidate)
            if target is None:
                continue
            try:
                shutil.rmtree(target)
            except FileNotFoundError:
                continue
            except OSError as err:
                return self._send(500, {"error": "delete failed", "detail": str(err),
                                        "node_id": node_id, "serials": removed})
            removed.append(name)
        return self._send(200, {"ok": True, "node_id": node_id,
                                "deleted": bool(removed), "serials": removed})

    def _delete_node_package(self, serial_dir, serial, node_id):
        target = _contained(serial_dir, os.path.join(serial_dir, node_id))
        if target is None:
            return self._send(400, {"error": "invalid node_id"})
        if not os.path.isdir(target):
            # Idempotent: a purge that is retried, or a node that never had a
            # package baked, must not fail. The caller cannot tell the two apart
            # and does not need to.
            return self._send(200, {"ok": True, "serial": serial, "node_id": node_id,
                                    "deleted": False})
        try:
            shutil.rmtree(target)
        except OSError as err:
            return self._send(500, {"error": "delete failed", "detail": str(err)})
        return self._send(200, {"ok": True, "serial": serial, "node_id": node_id,
                                "deleted": True})

    def _delete_legacy_remainder(self, serial_dir, serial):
        if not os.path.isdir(serial_dir):
            return self._send(200, {"ok": True, "serial": serial, "deleted": False,
                                    "legacy_files_deleted": 0})
        removed = 0
        node_dirs = 0
        try:
            with os.scandir(serial_dir) as entries:
                for entry in entries:
                    # A real directory is a node's package (or something this
                    # route does not own): counted, never touched. Anything else
                    # — a legacy package file, or a symlink, which is removed as
                    # the link itself and never followed — is the remainder.
                    if entry.is_dir(follow_symlinks=False):
                        node_dirs += 1
                        continue
                    os.unlink(entry.path)
                    removed += 1
        except OSError as err:
            return self._send(500, {"error": "delete failed", "detail": str(err)})
        if node_dirs:
            return self._send(409, {
                "error": "node packages remain",
                "serial": serial,
                "remaining": node_dirs,
                "legacy_files_deleted": removed,
            })
        try:
            os.rmdir(serial_dir)
        except FileNotFoundError:
            pass
        except OSError as err:
            # A bake landing between the scan and the rmdir leaves a non-empty
            # directory: somebody's live package, so it is the same refusal.
            return self._send(409, {"error": "node packages remain", "serial": serial,
                                    "remaining": 1, "legacy_files_deleted": removed,
                                    "detail": str(err)})
        return self._send(200, {"ok": True, "serial": serial, "deleted": True,
                                "legacy_files_deleted": removed})

    def log_message(self, fmt, *args):  # keep container logs quiet/structured
        pass


_outdir_locks = {}
_outdir_locks_guard = threading.Lock()


def _outdir_lock(outdir):
    """The lock that serializes builds into one package directory."""
    with _outdir_locks_guard:
        lock = _outdir_locks.get(outdir)
        if lock is None:
            lock = _outdir_locks[outdir] = threading.Lock()
        return lock


def _remove_stale_stages(outdir):
    """Remove staging directories a killed build left behind (sign.sh removes
    its own on exit; a timeout kills it before it can). Called under the
    directory's lock, so no live build owns one."""
    try:
        with os.scandir(outdir) as entries:
            stale = [e.path for e in entries
                     if e.name.startswith(".stage.") and e.is_dir(follow_symlinks=False)]
    except OSError:
        return
    for path in stale:
        shutil.rmtree(path, ignore_errors=True)


def _contained(base, path):
    """Return realpath(path) when it lies strictly inside base, else None."""
    target = os.path.realpath(path)
    if target == base or not target.startswith(base + os.sep):
        return None
    return target

def main():
    if not SIGNER_TOKEN:
        print(
            "FATAL: SIGNER_TOKEN is unset. This service signs EEPROM packages "
            "with the private key at /keys and must never accept unauthenticated "
            "requests. Set SIGNER_TOKEN (and the backend's EEPROM_SIGNER_TOKEN "
            "to the same value).",
            file=sys.stderr,
        )
        raise SystemExit(1)
    port = int(os.environ.get("PORT", "9099"))
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
