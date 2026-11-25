#!/usr/bin/env python3
"""Tests for check_licence_boundary.py — the map must be total, the Apache
directories pure, and a header must never contradict the map."""

from __future__ import annotations

import io
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import check_licence_boundary as clb  # noqa: E402

MAP = """version = 1

[[annotations]]
path = "**"
precedence = "override"
SPDX-FileCopyrightText = "x"
SPDX-License-Identifier = "LicenseRef-FCL-1.0-ALv2"

[[annotations]]
path = ["agent/**", "proto/**"]
precedence = "override"
SPDX-FileCopyrightText = "x"
SPDX-License-Identifier = "Apache-2.0"

[[annotations]]
path = "backend/copied/*.go"
precedence = "override"
SPDX-FileCopyrightText = "x"
SPDX-License-Identifier = "Apache-2.0"
"""


def run(root: Path, *extra: str) -> tuple[int, str]:
    out = io.StringIO()
    with redirect_stdout(out), redirect_stderr(out):
        rc = clb.main(["--root", str(root), *extra])
    return rc, out.getvalue()


class BoundaryTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "REUSE.toml").write_text(MAP)
        self.write("backend/a.go", "package a\n")
        self.write("agent/b.go", "package b\n")
        self.write("proto/c.proto", "syntax = \"proto3\";\n")
        self.write("backend/copied/r.go", "// SPDX-License-Identifier: Apache-2.0\npackage r\n")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def write(self, rel: str, text: str) -> None:
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def test_clean_tree_passes(self) -> None:
        rc, out = run(self.root, "--apache-dir", "agent", "--apache-dir", "proto")
        self.assertEqual(rc, 0, out)
        self.assertIn("Apache-2.0=3", out)
        self.assertIn("LicenseRef-FCL-1.0-ALv2=1", out)

    def test_header_disagreeing_with_map_fails(self) -> None:
        self.write("backend/a.go", "// SPDX-License-Identifier: MIT\npackage a\n")
        rc, out = run(self.root, "--apache-dir", "agent")
        self.assertEqual(rc, 1)
        self.assertIn("licence-header", out)

    def test_fcl_inside_apache_dir_fails(self) -> None:
        # The map says tools/ is FCL by default; declaring it an Apache dir
        # must therefore fail — that is the boundary the check exists for.
        self.write("tools/x.go", "package x\n")
        rc, out = run(self.root, "--apache-dir", "tools")
        self.assertEqual(rc, 1)
        self.assertIn("licence-boundary", out)

    def test_apache_island_file_is_asserted(self) -> None:
        # An island named as ONE file must be checked by equality; a map that
        # drops its annotation relicenses it to FCL and must fail.
        self.write("backend/island.go", "package island\n")
        rc, out = run(self.root, "--apache-dir", "agent", "--apache-dir", "backend/island.go")
        self.assertEqual(rc, 1)
        self.assertIn("backend/island.go:0:licence-boundary", out)

    def test_unmapped_path_fails(self) -> None:
        (self.root / "REUSE.toml").write_text(MAP.replace('path = "**"', 'path = "backend/**"'))
        self.write("other/z.txt", "no annotation reaches this path\n")
        rc, out = run(self.root, "--apache-dir", "agent")
        self.assertEqual(rc, 1)
        self.assertIn("licence-unmapped", out)

    def test_closest_precedence_is_refused(self) -> None:
        (self.root / "REUSE.toml").write_text(MAP.replace('precedence = "override"', 'precedence = "closest"', 1))
        rc, out = run(self.root)
        self.assertEqual(rc, 2)
        self.assertIn("override", out)

    def test_glob_semantics(self) -> None:
        self.assertTrue(clb.glob_matches("agent/**", "agent/pkg/x/y.go"))
        self.assertTrue(clb.glob_matches("backend/copied/*.go", "backend/copied/r.go"))
        self.assertFalse(clb.glob_matches("backend/copied/*.go", "backend/copied/deep/r.go"))
        self.assertTrue(clb.glob_matches("**", "anything/at/all"))
        self.assertTrue(clb.glob_matches("**/keys/**", "a/b/keys/k.pem"))

    def test_files_subset(self) -> None:
        rc, out = run(self.root, "--files", "agent/b.go", "--apache-dir", "agent")
        self.assertEqual(rc, 0, out)
        self.assertIn("Apache-2.0=1", out)


if __name__ == "__main__":
    unittest.main()
