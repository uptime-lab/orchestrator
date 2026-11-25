"""Tests for eeprom-signer/server.py DELETE /by-serial/...

Packages are keyed on the node: by-serial/<serial>/<node_id>/. A short serial is
shared by unrelated devices, so one serial directory can hold several tenants'
packages side by side. The purge deletes its OWN node's directory through
DELETE /by-serial/<serial>/<node_id>, and asks DELETE /by-serial/<serial> only
for the legacy files written before the node layout — which must never take a
node subdirectory with it. Stdlib `unittest` only, against a temp tree.

Run: python3 -m unittest discover -s eeprom-signer/tests
"""

import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import server  # noqa: E402

SERIAL = "abcdef12"
NODE_A = "00000000-0000-4000-8000-00000000000a"
NODE_B = "00000000-0000-4000-8000-00000000000b"


class SignerDeleteTest(unittest.TestCase):
    def setUp(self):
        self.token = "test-token"
        self._orig = (server.SIGNER_TOKEN, server.OUTPUT_ROOT)
        self.tmp = tempfile.TemporaryDirectory()
        server.SIGNER_TOKEN = self.token
        server.OUTPUT_ROOT = os.path.join(self.tmp.name, "output")
        self.by_serial = os.path.join(server.OUTPUT_ROOT, "by-serial")
        os.makedirs(self.by_serial)

    def tearDown(self):
        server.SIGNER_TOKEN, server.OUTPUT_ROOT = self._orig
        self.tmp.cleanup()

    def delete(self, path, *, authorized=True):
        h = server.Handler.__new__(server.Handler)
        h.path = path
        h.headers = {}
        if authorized:
            h.headers["Authorization"] = "Bearer " + self.token
        captured = {}

        def _send(code, obj):
            captured["code"] = code
            captured["obj"] = obj

        h._send = _send
        h.do_DELETE()
        return captured["code"], captured["obj"]

    def package(self, *parts):
        d = os.path.join(self.by_serial, *parts)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "pieeprom.upd"), "w") as f:
            f.write("token-bearing")
        return d

    # ---- the node route ----------------------------------------------

    def test_node_route_deletes_only_that_node(self):
        a = self.package(SERIAL, NODE_A)
        b = self.package(SERIAL, NODE_B)
        code, obj = self.delete(f"/by-serial/{SERIAL}/{NODE_B}")
        self.assertEqual(code, 200, obj)
        self.assertTrue(obj["deleted"])
        self.assertFalse(os.path.exists(b), "the named node's package survived")
        self.assertTrue(os.path.isdir(a), "another node's package under the same serial was deleted")

    def test_node_route_is_idempotent(self):
        for _ in range(2):
            code, obj = self.delete(f"/by-serial/{SERIAL}/{NODE_A}")
            self.assertEqual(code, 200, obj)
            self.assertFalse(obj["deleted"])

    def test_node_route_validates_the_node_id(self):
        self.package(SERIAL, NODE_A)
        for node in ("..", "%2e%2e", NODE_A.upper(), "x" * 36, "a%2Fb"):
            code, obj = self.delete(f"/by-serial/{SERIAL}/{node}")
            self.assertEqual(code, 400, "node %r accepted: %r" % (node, obj))
        self.assertTrue(os.path.isdir(os.path.join(self.by_serial, SERIAL, NODE_A)))

    def test_node_route_refuses_a_symlink_out_of_the_tree(self):
        outside = os.path.join(self.tmp.name, "elsewhere")
        os.makedirs(outside)
        with open(os.path.join(outside, "keep"), "w") as f:
            f.write("x")
        os.makedirs(os.path.join(self.by_serial, SERIAL))
        os.symlink(outside, os.path.join(self.by_serial, SERIAL, NODE_A))
        code, _ = self.delete(f"/by-serial/{SERIAL}/{NODE_A}")
        self.assertEqual(code, 400)
        self.assertTrue(os.path.exists(os.path.join(outside, "keep")))

    def test_a_symlinked_serial_dir_is_refused(self):
        outside = os.path.join(self.tmp.name, "elsewhere")
        os.makedirs(os.path.join(outside, NODE_A))
        os.symlink(outside, os.path.join(self.by_serial, SERIAL))
        code, _ = self.delete(f"/by-serial/{SERIAL}/{NODE_A}")
        self.assertEqual(code, 400)
        self.assertTrue(os.path.isdir(os.path.join(outside, NODE_A)))

    def test_extra_segments_are_not_found(self):
        code, _ = self.delete(f"/by-serial/{SERIAL}/{NODE_A}/pieeprom.upd")
        self.assertEqual(code, 404)

    def test_unauthorized(self):
        a = self.package(SERIAL, NODE_A)
        code, _ = self.delete(f"/by-serial/{SERIAL}/{NODE_A}", authorized=False)
        self.assertEqual(code, 401)
        self.assertTrue(os.path.isdir(a))

    # ---- the node-wide route: every serial directory --------------------

    def test_by_node_deletes_the_package_under_every_serial(self):
        # A WebUSB bake lands under the SHORT serial; the node is later rebound
        # to its FULL serial, which is all the purge knows. The route must find
        # the package wherever it was baked, and nothing else.
        full = "10000000" + SERIAL
        short_pkg = self.package(SERIAL, NODE_A)
        full_pkg = self.package(full, NODE_A)
        other = self.package(SERIAL, NODE_B)
        legacy = self.package(SERIAL)
        code, obj = self.delete(f"/by-node/{NODE_A}")
        self.assertEqual(code, 200, obj)
        self.assertTrue(obj["deleted"])
        self.assertEqual(sorted(obj["serials"]), sorted([SERIAL, full]))
        self.assertFalse(os.path.exists(short_pkg))
        self.assertFalse(os.path.exists(full_pkg))
        self.assertTrue(os.path.isdir(other), "another node's package was deleted")
        self.assertTrue(os.path.exists(os.path.join(legacy, "pieeprom.upd")),
                        "the node route took a legacy file")

    def test_by_node_is_idempotent_and_reports_absent(self):
        self.package(SERIAL, NODE_A)
        code, obj = self.delete(f"/by-node/{NODE_A}")
        self.assertEqual((code, obj["deleted"]), (200, True), obj)
        code, obj = self.delete(f"/by-node/{NODE_A}")
        self.assertEqual(code, 200, obj)
        self.assertFalse(obj["deleted"])
        self.assertEqual(obj["serials"], [])

    def test_by_node_with_no_tree_is_absent(self):
        os.rmdir(self.by_serial)
        code, obj = self.delete(f"/by-node/{NODE_A}")
        self.assertEqual(code, 200, obj)
        self.assertEqual((obj["deleted"], obj["serials"]), (False, []))

    def test_by_node_validates_the_node_id(self):
        self.package(SERIAL, NODE_A)
        for node in ("..", "%2e%2e", NODE_A.upper(), "x" * 36, "a%2Fb", ""):
            code, obj = self.delete(f"/by-node/{node}")
            self.assertEqual(code, 400, "node %r accepted: %r" % (node, obj))
        code, _ = self.delete(f"/by-node/{NODE_A}/pieeprom.upd")
        self.assertEqual(code, 404)
        self.assertTrue(os.path.isdir(os.path.join(self.by_serial, SERIAL, NODE_A)))

    def test_by_node_never_follows_a_symlink(self):
        outside = os.path.join(self.tmp.name, "elsewhere")
        os.makedirs(os.path.join(outside, NODE_A))
        with open(os.path.join(outside, NODE_A, "keep"), "w") as f:
            f.write("x")
        # A symlinked serial directory pointing out of the tree ...
        os.symlink(outside, os.path.join(self.by_serial, SERIAL))
        # ... and a symlink named like the node inside a real serial directory.
        real = os.path.join(self.by_serial, "deadbeef")
        os.makedirs(real)
        os.symlink(os.path.join(outside, NODE_A), os.path.join(real, NODE_A))
        code, obj = self.delete(f"/by-node/{NODE_A}")
        self.assertEqual(code, 200, obj)
        self.assertFalse(obj["deleted"])
        self.assertTrue(os.path.exists(os.path.join(outside, NODE_A, "keep")))

    def test_by_node_unauthorized(self):
        a = self.package(SERIAL, NODE_A)
        code, _ = self.delete(f"/by-node/{NODE_A}", authorized=False)
        self.assertEqual(code, 401)
        self.assertTrue(os.path.isdir(a))

    # ---- the serial route: legacy remainder only -----------------------

    def test_serial_route_refuses_while_node_packages_remain(self):
        legacy = self.package(SERIAL)  # legacy files directly under the serial
        a = self.package(SERIAL, NODE_A)
        code, obj = self.delete(f"/by-serial/{SERIAL}")
        self.assertEqual(code, 409, obj)
        self.assertEqual(obj["remaining"], 1)
        self.assertEqual(obj["legacy_files_deleted"], 1)
        self.assertTrue(os.path.isdir(a), "the whole-serial route took a node's package")
        self.assertFalse(os.path.exists(os.path.join(legacy, "pieeprom.upd")))

    def test_serial_route_removes_legacy_files_and_the_empty_dir(self):
        legacy = self.package(SERIAL)
        code, obj = self.delete(f"/by-serial/{SERIAL}")
        self.assertEqual(code, 200, obj)
        self.assertTrue(obj["deleted"])
        self.assertFalse(os.path.exists(legacy))

    def test_serial_route_is_idempotent(self):
        for _ in range(2):
            code, obj = self.delete(f"/by-serial/{SERIAL}")
            self.assertEqual(code, 200, obj)

    def test_serial_route_removes_a_symlink_without_following_it(self):
        outside = os.path.join(self.tmp.name, "elsewhere")
        os.makedirs(outside)
        with open(os.path.join(outside, "keep"), "w") as f:
            f.write("x")
        d = os.path.join(self.by_serial, SERIAL)
        os.makedirs(d)
        os.symlink(outside, os.path.join(d, "link"))
        code, obj = self.delete(f"/by-serial/{SERIAL}")
        self.assertEqual(code, 200, obj)
        self.assertTrue(os.path.exists(os.path.join(outside, "keep")))
        self.assertFalse(os.path.exists(d))

    def test_serial_route_validates_the_serial(self):
        for serial in ("..", "%2e%2e", "xyz", "10000000abcd1234abcd"):
            code, _ = self.delete(f"/by-serial/{serial}")
            self.assertEqual(code, 400, "serial %r accepted" % serial)
        self.assertTrue(os.path.isdir(self.by_serial))


if __name__ == "__main__":
    unittest.main()
