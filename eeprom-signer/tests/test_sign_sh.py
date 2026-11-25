"""Tests for eeprom-signer/sign.sh input validation and injection safety.

sign.sh must treat every value as data: `set_var` never interpolates a value
into a `sed` program (`sed -i "s|^${key}=.*|${key}=${val}|"` would let a
`|`-delimited value rewrite other config lines and GNU sed's `e` flag execute
a command beside the private key), and BOOT_ORDER and HTTP_HOST are anchored
over the whole string, not only their prefix.

These tests drive `bash sign.sh` with stubbed rpi-eeprom-config /
rpi-eeprom-digest tools (placed where the script looks for them by default) so
the whole flow runs without the real toolchain. Stdlib `unittest` only — pytest
is deliberately not installed in the signer image.

Run: python3 -m unittest discover -s eeprom-signer/tests
"""

import hashlib
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SIGN_SH = os.path.join(os.path.dirname(HERE), "sign.sh")

# The stub rpi-eeprom-config. Invoked two ways by sign.sh:
#   python3 CFG <bin>                       -> dump a config to stdout
#   python3 CFG --config C --digest D --pubkey P --out O <bin>  -> copy C to O
# In single-arg dump mode it echoes the file if it already looks like a config
# (the final pieeprom.upd re-dump), else a canned base config (the first dump
# off the raw firmware .bin).
STUB_CFG = r'''
import sys, os
args = sys.argv[1:]
if "--out" in args:
    conf = args[args.index("--config") + 1]
    out = args[args.index("--out") + 1]
    with open(conf) as f:
        data = f.read()
    with open(out, "w") as f:
        f.write(data)
elif len(args) == 1:
    path = args[0]
    data = open(path).read() if os.path.exists(path) else ""
    if "BOOT_ORDER=" in data:
        sys.stdout.write(data)
    else:
        sys.stdout.write(
            "BOOT_ORDER=0x0\nHTTP_HOST=placeholder\nPCIE_PROBE=0\n"
            "NET_INSTALL_AT_POWER_ON=0\nHTTP_PATH=old\nUPTIME_TOKEN=old\n"
        )
else:
    sys.stderr.write("stub rpi-eeprom-config: unexpected args %r\n" % (args,))
    sys.exit(3)
'''

# The stub rpi-eeprom-digest. Invoked as: sh DIG -i in -o out -k key -> touch out.
STUB_DIG = r'''#!/bin/sh
out=""
while [ $# -gt 0 ]; do
    case "$1" in
        -o) out="$2"; shift 2 ;;
        *) shift ;;
    esac
done
: > "$out"
'''


def _sed_is_gnu():
    try:
        r = subprocess.run(["sed", "--version"], capture_output=True, text=True)
    except OSError:
        return False
    return "GNU" in (r.stdout + r.stderr)


class SignShTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp(prefix="eeprom-signer-test-")
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)

        self.eeprom_dir = os.path.join(self.root, "eeprom-build")
        tools = os.path.join(self.eeprom_dir, "tools")
        os.makedirs(tools)
        # cm4 -> firmware-2711, cm5 -> firmware-2712.
        for fwdir in ("firmware-2711", "firmware-2712"):
            latest = os.path.join(self.eeprom_dir, "rpi-eeprom", fwdir, "latest")
            os.makedirs(latest)
            # A base .bin that does NOT look like a config (no BOOT_ORDER=).
            with open(os.path.join(latest, "pieeprom-2024-01-01.bin"), "wb") as f:
                f.write(b"\x00rpi-eeprom-base\x00")
            with open(os.path.join(latest, "recovery.bin"), "wb") as f:
                f.write(b"\x00recovery\x00")

        with open(os.path.join(tools, "rpi-eeprom-config"), "w") as f:
            f.write(STUB_CFG)
        dig = os.path.join(tools, "rpi-eeprom-digest")
        with open(dig, "w") as f:
            f.write(STUB_DIG)
        os.chmod(dig, 0o755)

        self.keys = os.path.join(self.root, "keys")
        os.makedirs(self.keys)
        for name in ("privkey.pem", "pubkey.pem"):
            with open(os.path.join(self.keys, name), "w") as f:
                f.write("-----STUB KEY-----\n")

        self.outdir = os.path.join(self.root, "out")

    def run_sign(self, *, soc="cm4", serial="10000000abcd1234",
                 http_host="boot.example.com", adoption_b64="YWRvcHQ=",
                 boot_order=None, outdir=None):
        env = dict(os.environ)
        env.update({
            "SOC": soc,
            "SERIAL": serial,
            "HTTP_HOST": http_host,
            "ADOPTION_B64": adoption_b64,
            "OUTDIR": outdir or self.outdir,
            "EEPROM_DIR": self.eeprom_dir,
            "PRIVKEY": os.path.join(self.keys, "privkey.pem"),
            "PUBKEY": os.path.join(self.keys, "pubkey.pem"),
        })
        if boot_order is not None:
            env["BOOT_ORDER"] = boot_order
        return subprocess.run(
            ["bash", SIGN_SH], env=env, capture_output=True, text=True, timeout=60,
        )

    def dump_upd(self, outdir=None):
        path = os.path.join(outdir or self.outdir, "pieeprom.upd")
        with open(path) as f:
            return f.read()

    # ---- legitimate values still work -----------------------------------

    def test_legit_default_boot_order(self):
        r = self.run_sign()
        self.assertEqual(r.returncode, 0, r.stderr)
        upd = self.dump_upd()
        self.assertIn("BOOT_ORDER=0xf1467", upd)
        self.assertIn("HTTP_HOST=boot.example.com", upd)
        self.assertIn("HTTP_PATH=http-boot/10000000abcd1234", upd)

    def test_legit_explicit_boot_orders(self):
        for bo in ("0xf1467", "0xf167", "0x1"):
            r = self.run_sign(boot_order=bo)
            self.assertEqual(r.returncode, 0, "%s: %s" % (bo, r.stderr))
            self.assertIn("BOOT_ORDER=%s" % bo, self.dump_upd())

    def test_legit_cm5_and_ipv4_host(self):
        r = self.run_sign(soc="cm5", http_host="192.0.2.10")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("HTTP_HOST=192.0.2.10", self.dump_upd())

    # ---- BOOT_ORDER injection is rejected -------------------------------

    def test_boot_order_sed_injection_rejected(self):
        # A payload that passes a prefix-only glob and would rewrite HTTP_HOST
        # through a sed program. Assert the GUARD fired (its stderr), not merely
        # a non-zero exit — a script without the guard also exits non-zero here
        # on BSD sed, for the wrong reason, so the stderr check is what makes
        # this test fail without the guard.
        r = self.run_sign(boot_order="0x1|;s|^HTTP_HOST=.*|HTTP_HOST=attacker.evil")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("invalid BOOT_ORDER", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.outdir, "pieeprom.upd")))

    def test_boot_order_malformed_rejected(self):
        for bo in ("1467", "0x", "0xGG", "0x123456789", "0x1 rm", "0x1&", "0x1\\",
                   "0x1\ntouch x", "0x1|foo"):
            r = self.run_sign(boot_order=bo)
            self.assertNotEqual(r.returncode, 0, "accepted bad BOOT_ORDER %r" % bo)

    # ---- HTTP_HOST injection is rejected --------------------------------

    def test_http_host_sed_injection_rejected(self):
        # Assert the guard's stderr: without the guard the value reaches the
        # config rewrite and the script fails later, for another reason.
        r = self.run_sign(http_host="x|;s|^BOOT_ORDER=.*|BOOT_ORDER=0x0|")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("invalid HTTP_HOST", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.outdir, "pieeprom.upd")))

    def test_http_host_malformed_rejected(self):
        for host in ("has space", "a;b", "$(id)", "`id`", ".leadingdot",
                     "trailingdot.", "a\nb", "back\\slash", "a|b", ""):
            r = self.run_sign(http_host=host)
            self.assertNotEqual(r.returncode, 0, "accepted bad HTTP_HOST %r" % host)

    # ---- ADOPTION_B64 is one config line of base64 ---------------------

    def test_adoption_line_injection_rejected(self):
        r = self.run_sign(adoption_b64="abc\nSIGNED_BOOT=1\nHTTP_HOST=evil.example")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("invalid ADOPTION_B64", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.outdir, "pieeprom.upd")))

    def test_adoption_malformed_rejected(self):
        for blob in ("YWRvcHQ", "YW Rv", "a|b=", "$(id)", "YW=v", "A" * 1540):
            r = self.run_sign(adoption_b64=blob)
            self.assertNotEqual(r.returncode, 0, "accepted bad ADOPTION_B64 %r" % blob[:20])
            self.assertIn("invalid ADOPTION_B64", r.stderr)

    def test_adoption_long_blob_accepted(self):
        r = self.run_sign(adoption_b64="QUJD" * 384)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("UPTIME_TOKEN=" + "QUJD" * 384, self.dump_upd())

    # ---- set_var treats the value as data, not a sed program ------------

    @unittest.skipUnless(
        _sed_is_gnu(),
        "GNU sed required: the s///e command-execution escalation cannot be "
        "reproduced with BSD sed (e.g. macOS); the delimiter/newline guard "
        "tests above still run here.",
    )
    def test_setvar_value_is_not_executed(self):
        # Craft a value that a sed-based set_var would hand to GNU sed as a
        # command (the `e` command runs the argument per line). The script
        # refuses it as ADOPTION_B64 before any rewrite; either way nothing
        # may run.
        sentinel = os.path.join(self.root, "SENTINEL")
        payload = "X|\ne touch %s" % sentinel
        self.run_sign(adoption_b64=payload)
        self.assertFalse(
            os.path.exists(sentinel),
            "set_var executed the value as a command (sentinel created)",
        )

    # ---- the served directory only ever holds a whole package -----------

    def _package_is_consistent(self):
        """Every file the manifest names matches it, as the agent checks."""
        with open(os.path.join(self.outdir, "SHA256SUMS")) as f:
            for line in f:
                want, name = line.split()
                with open(os.path.join(self.outdir, name), "rb") as g:
                    if hashlib.sha256(g.read()).hexdigest() != want:
                        return False
        return True

    def test_a_failed_rebuild_leaves_the_previous_package_whole(self):
        # The previous build is what a device may be downloading right now.
        r = self.run_sign(adoption_b64="Zmlyc3Q=")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(self._package_is_consistent())
        before = self.dump_upd()

        # A rebuild that dies after the new image exists and before its
        # manifest does: the state a reader sees mid-build, made permanent.
        dig = os.path.join(self.eeprom_dir, "tools", "rpi-eeprom-digest")
        with open(dig, "w") as f:
            f.write(STUB_DIG.replace(
                ': > "$out"',
                'case "$out" in *pieeprom.sig) exit 1 ;; esac\n: > "$out"'))
        r = self.run_sign(adoption_b64="c2Vjb25k")
        self.assertNotEqual(r.returncode, 0)

        self.assertEqual(self.dump_upd(), before,
                         "the served image was replaced by a build that never finished")
        self.assertTrue(self._package_is_consistent(),
                        "the served manifest no longer matches the served image")
        self.assertEqual([n for n in os.listdir(self.outdir) if n.startswith(".")], [],
                         "a staging directory was left behind")

    def test_a_rebuild_replaces_the_whole_package(self):
        self.assertEqual(self.run_sign(adoption_b64="Zmlyc3Q=").returncode, 0)
        r = self.run_sign(adoption_b64="c2Vjb25k")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("UPTIME_TOKEN=c2Vjb25k", self.dump_upd())
        self.assertTrue(self._package_is_consistent())
        self.assertEqual(sorted(os.listdir(self.outdir)),
                         ["SHA256SUMS", "pieeprom.sig", "pieeprom.upd", "recovery.bin"])


if __name__ == "__main__":
    unittest.main()
