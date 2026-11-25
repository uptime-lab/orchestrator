"""Tests for notices.py: classification, the copyleft refusal, the pnpm walk and
the rendering that groups identical texts."""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import notices  # noqa: E402

MIT = "MIT License\n\nCopyright (c) 2020 Someone\n\n" + notices.MIT_PERMISSION
BSD3 = """Copyright (c) 2009 The Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:
Neither the name of the copyright holder nor the names of its contributors
may be used to endorse or promote products derived from this software."""
GPL = "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007"


class ClassifyTest(unittest.TestCase):
    def test_kinds(self) -> None:
        self.assertEqual(notices.classify(MIT), "MIT")
        self.assertEqual(notices.classify(BSD3), "BSD-3-Clause")
        self.assertEqual(notices.classify(GPL), "copyleft")
        self.assertEqual(notices.classify("Apache License\nVersion 2.0, January 2004"), "Apache-2.0")

    def test_normalize_trims_and_unifies_line_ends(self) -> None:
        self.assertEqual(notices.normalize(b"\r\n a  \r\nb\t\r\n\r\n"), " a\nb")

    def test_spdx_expression(self) -> None:
        self.assertTrue(notices.spdx_permits("MIT"))
        self.assertTrue(notices.spdx_permits("(MIT OR GPL-3.0-only)"))
        self.assertFalse(notices.spdx_permits("GPL-3.0-only"))
        self.assertFalse(notices.spdx_permits("MIT AND GPL-2.0-only"))


class PolicyTest(unittest.TestCase):
    def test_copyleft_text_is_refused(self) -> None:
        section = notices.Section("Go", [notices.Component("x", "v1", "copyleft", [("COPYING", GPL)])])
        with self.assertRaises(notices.RefusedLicence):
            notices.enforce_policy([section])

    def test_dual_licence_offering_a_permissive_choice_passes(self) -> None:
        comp = notices.Component("y", "1.0.0", "(MIT OR GPL-3.0-only)", [("LICENSE", GPL)])
        notices.enforce_policy([notices.Section(notices.NPM_TITLE, [comp])])


class RenderTest(unittest.TestCase):
    def test_identical_texts_are_printed_once(self) -> None:
        a = notices.Component("a", "v1", "MIT", [("LICENSE", MIT)])
        b = notices.Component("b", "v2", "MIT", [("LICENSE", MIT)])
        out = notices.render("T", "intro", [notices.Section("S", [a, b])], "")
        self.assertEqual(out.count("Copyright (c) 2020 Someone"), 1)
        self.assertIn("Carried by: `a@v1`, `b@v2`.", out)

    def test_fence_outgrows_the_text(self) -> None:
        self.assertEqual(notices.fence_for("x ~~~~ y"), "~~~~~")


class PnpmWalkTest(unittest.TestCase):
    """A two-package pnpm layout: app -> dep-a -> dep-b, plus a devDependency
    that must not be walked."""

    def make_pkg(self, store: Path, name: str, version: str, deps: dict[str, str], licence_text: str | None) -> Path:
        pkg = store / f"{name}@{version}" / "node_modules" / name
        pkg.mkdir(parents=True)
        (pkg / "package.json").write_text(json.dumps(
            {"name": name, "version": version, "license": "MIT", "dependencies": deps}))
        if licence_text is not None:
            (pkg / "LICENSE").write_text(licence_text)
        return pkg

    def test_walk(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fe = root / "frontend"
            store = fe / "node_modules" / ".pnpm"
            store.mkdir(parents=True)
            (fe / "package.json").write_text(json.dumps(
                {"dependencies": {"dep-a": "1"}, "devDependencies": {"dev-x": "1"}}))
            a = self.make_pkg(store, "dep-a", "1.0.0", {"dep-b": "2"}, MIT)
            b = self.make_pkg(store, "dep-b", "2.0.0", {}, None)
            self.make_pkg(store, "dev-x", "1.0.0", {}, MIT)
            (fe / "node_modules" / "dep-a").symlink_to(a)
            (a.parent / "dep-b").symlink_to(b)
            section = notices.npm_section(root)
            names = [(c.name, c.version) for c in section.components]
            self.assertEqual(names, [("dep-a", "1.0.0"), ("dep-b", "2.0.0")])
            self.assertIn("ships no licence file", section.components[1].note)

    def test_missing_install_is_an_input_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / "frontend").mkdir()
            with self.assertRaises(notices.InputError):
                notices.npm_section(Path(tmp))

class MirrorRootTest(unittest.TestCase):
    """The mirror's copies exist only where the mirror is assembled."""

    def test_twins_are_not_demanded_without_a_mirror_root(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "agent").mkdir()
            (root / "agent" / "NOTICE").write_text("notice\n")
            self.assertFalse(notices.has_mirror_root(root))
            self.assertEqual(notices.check_twins(root), [])

    def test_twins_are_compared_where_a_mirror_root_exists(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for d in ("agent", notices.MIRROR_ROOT):
                (root / d).mkdir()
            (root / "agent" / "NOTICE").write_text("notice\n")
            self.assertEqual(len(notices.check_twins(root)), 1)  # the twin is missing
            (root / notices.MIRROR_ROOT / "NOTICE").write_text("another\n")
            self.assertEqual(len(notices.check_twins(root)), 1)  # the twin differs
            (root / notices.MIRROR_ROOT / "NOTICE").write_text("notice\n")
            self.assertEqual(notices.check_twins(root), [])


if __name__ == "__main__":
    unittest.main()
