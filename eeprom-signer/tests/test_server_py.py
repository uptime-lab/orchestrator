"""Tests for eeprom-signer/server.py POST /sign input validation.

Every /sign field reaches the EEPROM config that sign.sh writes, so do_POST
must validate `http_host` and `boot_order` as tightly as `serial` and `soc`.
These tests exercise the handler's validation directly (subprocess mocked) and
confirm malformed values are refused with 400 before the signer ever runs,
while legitimate values reach the subprocess. Stdlib `unittest` only.

Run: python3 -m unittest discover -s eeprom-signer/tests
"""

import io
import json
import os
import shutil
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import server  # noqa: E402


class FakeRun:
    """Stand-in for subprocess.run; records the env it was called with."""

    def __init__(self):
        self.calls = []

    def __call__(self, *args, **kwargs):
        self.calls.append(kwargs.get("env", {}))

        class Result:
            returncode = 0
            stdout = ""
            stderr = ""

        return Result()


class ServerValidationTest(unittest.TestCase):
    def setUp(self):
        self.token = "test-token"
        self._orig = {
            "SIGNER_TOKEN": server.SIGNER_TOKEN,
            "HTTP_HOST_DEFAULT": server.HTTP_HOST_DEFAULT,
            "BOOT_ORDER_DEFAULT": server.BOOT_ORDER_DEFAULT,
            "run": server.subprocess.run,
        }
        server.SIGNER_TOKEN = self.token
        server.HTTP_HOST_DEFAULT = "boot.example.com"
        server.BOOT_ORDER_DEFAULT = "0xf1467"
        self.fake_run = FakeRun()
        server.subprocess.run = self.fake_run

    def tearDown(self):
        server.SIGNER_TOKEN = self._orig["SIGNER_TOKEN"]
        server.HTTP_HOST_DEFAULT = self._orig["HTTP_HOST_DEFAULT"]
        server.BOOT_ORDER_DEFAULT = self._orig["BOOT_ORDER_DEFAULT"]
        server.subprocess.run = self._orig["run"]

    def post(self, body, *, authorized=True):
        """Drive Handler.do_POST without a live socket. Returns (status, obj)."""
        h = server.Handler.__new__(server.Handler)
        raw = json.dumps(body).encode()
        h.path = "/sign"
        h.headers = {"Content-Length": str(len(raw))}
        if authorized:
            h.headers["Authorization"] = "Bearer " + self.token
        h.rfile = io.BytesIO(raw)
        captured = {}

        def _send(code, obj):
            captured["code"] = code
            captured["obj"] = obj

        h._send = _send
        h.do_POST()
        return captured["code"], captured["obj"]

    def valid_body(self, **over):
        body = {
            "soc": "cm5",
            "serial": "10000000abcd1234",
            "http_host": "boot.example.com",
            "adoption_b64": "YWRvcHQ=",
            "boot_order": "0xf1467",
        }
        body.update(over)
        return body

    # ---- happy path reaches the signer ----------------------------------

    def test_legit_request_calls_signer(self):
        code, _ = self.post(self.valid_body())
        self.assertEqual(code, 200)
        self.assertEqual(len(self.fake_run.calls), 1)
        env = self.fake_run.calls[0]
        self.assertEqual(env["SOC"], "cm5")
        self.assertEqual(env["SERIAL"], "10000000abcd1234")
        self.assertEqual(env["HTTP_HOST"], "boot.example.com")
        self.assertEqual(env["BOOT_ORDER"], "0xf1467")

    def test_defaults_used_when_omitted(self):
        body = self.valid_body()
        del body["http_host"]
        del body["boot_order"]
        code, _ = self.post(body)
        self.assertEqual(code, 200)
        env = self.fake_run.calls[0]
        self.assertEqual(env["HTTP_HOST"], "boot.example.com")
        self.assertEqual(env["BOOT_ORDER"], "0xf1467")

    def test_ipv4_host_allowed(self):
        code, _ = self.post(self.valid_body(http_host="192.0.2.10"))
        self.assertEqual(code, 200)

    # ---- malformed values are refused before the signer runs ------------

    def _assert_rejected(self, body, field_hint):
        code, obj = self.post(body)
        self.assertEqual(code, 400, "%s accepted: %r" % (field_hint, obj))
        self.assertEqual(self.fake_run.calls, [], "signer ran on bad input")

    def test_boot_order_injection_rejected(self):
        self._assert_rejected(
            self.valid_body(boot_order="0x1|;s|^HTTP_HOST=.*|HTTP_HOST=evil"),
            "boot_order",
        )

    def test_boot_order_malformed_rejected(self):
        for bo in ("1467", "0x", "0xGG", "0x123456789", "0x1 rm", "0x1\ntouch"):
            self._assert_rejected(self.valid_body(boot_order=bo), "boot_order %r" % bo)

    def test_http_host_injection_rejected(self):
        self._assert_rejected(
            self.valid_body(http_host="x|;s|^BOOT_ORDER=.*|BOOT_ORDER=0x0|"),
            "http_host",
        )

    def test_http_host_malformed_rejected(self):
        for host in ("has space", "a;b", "$(id)", ".lead", "trail.", "a\nb", "a|b"):
            self._assert_rejected(self.valid_body(http_host=host), "http_host %r" % host)

    # ---- soc / serial remain enforced -----------------------------------

    def test_bad_soc_rejected(self):
        self._assert_rejected(self.valid_body(soc="cm6"), "soc")

    def test_bad_serial_rejected(self):
        for serial in ("..", "xyz", "10000000abcd1234abcd", "../etc"):
            self._assert_rejected(self.valid_body(serial=serial), "serial %r" % serial)

    def test_missing_adoption_rejected(self):
        body = self.valid_body()
        del body["adoption_b64"]
        self._assert_rejected(body, "adoption_b64")

    def test_adoption_injection_rejected(self):
        # UPTIME_TOKEN is one line of the signed bootloader configuration; a
        # line break in the value would add lines of the caller's choosing.
        self._assert_rejected(
            self.valid_body(adoption_b64="YWRvcHQ=\nSIGNED_BOOT=1\nHTTP_HOST=evil"),
            "adoption_b64",
        )

    def test_adoption_malformed_rejected(self):
        for blob in ("YWRvcHQ", "YW Rv", "YWRv\r", "a|b=", "$(id)", "YW=v",
                     "A" * 1540):
            self._assert_rejected(self.valid_body(adoption_b64=blob), "adoption_b64 %r" % blob[:20])

    def test_adoption_long_blob_accepted(self):
        code, obj = self.post(self.valid_body(adoption_b64="QUJD" * 384))
        self.assertEqual(code, 200, obj)

    # ---- node-scoped output ------------------------------------------

    def test_node_id_scopes_the_output_directory(self):
        node = "00000000-0000-4000-8000-00000000000a"
        code, obj = self.post(self.valid_body(serial="abcdef12", node_id=node))
        self.assertEqual(code, 200)
        self.assertEqual(obj["path"], "eeprom/by-serial/abcdef12/" + node)
        self.assertTrue(self.fake_run.calls[0]["OUTDIR"].endswith("/by-serial/abcdef12/" + node))

    def test_without_node_id_keeps_the_serial_directory(self):
        code, obj = self.post(self.valid_body(serial="abcdef12"))
        self.assertEqual(code, 200)
        self.assertEqual(obj["path"], "eeprom/by-serial/abcdef12")

    def test_bad_node_id_rejected(self):
        for node in ("..", "../x", "6F0D2F2E-6A4E-4B8E-9D55-0B1F8F3B8D11", "x" * 36, "a/b"):
            self._assert_rejected(self.valid_body(node_id=node), "node_id %r" % node)

    def test_unauthorized_rejected(self):
        code, _ = self.post(self.valid_body(), authorized=False)
        self.assertEqual(code, 401)
        self.assertEqual(self.fake_run.calls, [])


class SerializedBuildTest(unittest.TestCase):
    """Builds into one package directory never overlap: sign.sh publishes by
    renaming files into place, and two interleaved builds would leave one
    build's manifest beside the other's image."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="eeprom-signer-serial-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self._orig = (server.SIGNER_TOKEN, server.OUTPUT_ROOT, server.subprocess.run)
        server.SIGNER_TOKEN = "t"
        server.OUTPUT_ROOT = self.tmp
        self.guard = threading.Lock()
        self.in_flight = {}
        self.max_in_flight = {}
        server.subprocess.run = self._slow_run

    def tearDown(self):
        server.SIGNER_TOKEN, server.OUTPUT_ROOT, server.subprocess.run = self._orig

    def _slow_run(self, *args, **kwargs):
        outdir = kwargs["env"]["OUTDIR"]
        with self.guard:
            self.in_flight[outdir] = self.in_flight.get(outdir, 0) + 1
            self.max_in_flight[outdir] = max(self.max_in_flight.get(outdir, 0),
                                             self.in_flight[outdir])
        time.sleep(0.1)
        with self.guard:
            self.in_flight[outdir] -= 1

        class Result:
            returncode = 0
            stdout = ""
            stderr = ""

        return Result()

    def _post(self, node_id):
        h = server.Handler.__new__(server.Handler)
        raw = json.dumps({"soc": "cm5", "serial": "abcdef12", "http_host": "boot.example.com",
                          "adoption_b64": "YQ==", "node_id": node_id}).encode()
        h.path = "/sign"
        h.headers = {"Content-Length": str(len(raw)), "Authorization": "Bearer t"}
        h.rfile = io.BytesIO(raw)
        h._send = lambda code, obj: None
        h.do_POST()

    def _race(self, node_ids):
        threads = [threading.Thread(target=self._post, args=(n,)) for n in node_ids]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

    def test_two_builds_for_one_node_do_not_overlap(self):
        node = "00000000-0000-4000-8000-00000000000a"
        self._race([node, node])
        self.assertEqual(list(self.max_in_flight.values()), [1])

    def test_builds_for_different_nodes_still_run_together(self):
        self._race(["00000000-0000-4000-8000-00000000000a",
                    "00000000-0000-4000-8000-00000000000b"])
        self.assertEqual(sorted(self.max_in_flight.values()), [1, 1])

    def test_a_killed_build_leaves_no_stage_behind_for_the_next(self):
        node = "00000000-0000-4000-8000-00000000000a"
        stale = os.path.join(self.tmp, "by-serial", "abcdef12", node, ".stage.dead")
        os.makedirs(stale)
        self._post(node)
        self.assertFalse(os.path.exists(stale))


if __name__ == "__main__":
    unittest.main()
