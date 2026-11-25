#!/usr/bin/env python3
"""Tests for check_doc_citations.py."""

from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import check_doc_citations as cdc  # noqa: E402

# Fixtures are assembled at runtime so this file carries no citation shape of
# its own: the checker scans its own directory in the tree it ships in.
D = "docs" + "/"


def cite(name: str) -> str:
    return D + name


def tick(name: str) -> str:
    return "`" + name + "`"


def see(name: str) -> str:
    return "see " + name


def missing(path: str, line: int, name: str) -> str:
    return f"{path}:{line}:doc-citation: {cite(name)} does not exist"


def make_tree(tmp: str, files: dict[str, str]) -> Path:
    root = Path(tmp)
    for rel, text in files.items():
        full = root / rel
        full.parent.mkdir(parents=True, exist_ok=True)
        full.write_text(text, encoding="utf-8")
    return root


class Citations(unittest.TestCase):
    def test_path_citation_to_missing_doc_trips(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = make_tree(tmp, {"pkg/a.go": f"// see {cite('MISSING-PLAN.md')} for the design\n", cite("PRESENT.md"): "x\n"})
            self.assertEqual([missing("pkg/a.go", 1, "MISSING-PLAN.md")], cdc.check(root, set()))

    def test_path_citation_to_existing_doc_passes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = make_tree(tmp, {"pkg/a.go": f"// see {cite('PRESENT.md')}#section\n", cite("PRESENT.md"): "x\n"})
            self.assertEqual([], cdc.check(root, set()))

    def test_bare_citation_resolves_against_docs_own_dir_and_root(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = make_tree(tmp, {
                "README.md": f"read {tick('ROOT-NOTES.md')} and {tick('SUBDIR-NOTES.md')}, {see('GUIDE.md')}\n",
                "ROOT-NOTES.md": "x\n",
                cite("SUBDIR-NOTES.md"): "x\n",
                "tools/x/README.md": f"{see('LOCAL-NOTES.md')}\n",
                "tools/x/LOCAL-NOTES.md": "x\n",
            })
            self.assertEqual([missing("README.md", 1, "GUIDE.md")], cdc.check(root, set()))

    def test_lowercase_and_prefixed_names_are_not_citations(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = make_tree(tmp, {"a.md": f"`readme.md` see notes.md and ./other/{cite('X.md')} and my{cite('Y.md')}\n"})
            self.assertEqual([], cdc.check(root, set()))

    def test_allow_file_silences_a_named_citation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = make_tree(tmp, {"a.md": f"{cite('PRIVATE.md')} and {cite('ALSO-MISSING.md')}\n", "allow.txt": "PRIVATE.md  # kept private\n"})
            allow = cdc.read_allow(root / "allow.txt")
            self.assertEqual([missing("a.md", 1, "ALSO-MISSING.md")], cdc.check(root, allow))

    def test_skipped_and_binary_files_are_ignored(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            gone = cite("MISSING.md") + "\n"
            root = make_tree(tmp, {"node_modules/x/a.md": gone, "go.sum": gone, "x.pb.go": gone})
            (root / "blob.bin").write_bytes(gone.encode() + b"\x00\x01")
            self.assertEqual([], cdc.check(root, set()))


class CLI(unittest.TestCase):
    def run_cli(self, *argv: str) -> tuple[int, str]:
        proc = subprocess.run([sys.executable, str(HERE / "check_doc_citations.py"), *argv], capture_output=True, text=True)
        return proc.returncode, proc.stdout

    def test_exit_codes_and_github_format(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            make_tree(tmp, {"a.md": cite("MISSING.md") + "\n"})
            code, out = self.run_cli("--root", tmp)
            self.assertEqual((1, missing("a.md", 1, "MISSING.md") + "\n"), (code, out))
            code, out = self.run_cli("--root", tmp, "--format", "github")
            self.assertEqual(f"::error file=a.md,line=1,title=doc-citation::{cite('MISSING.md')} does not exist\n", out)
        with tempfile.TemporaryDirectory() as tmp:
            make_tree(tmp, {"a.md": "nothing cited\n"})
            self.assertEqual((0, ""), self.run_cli("--root", tmp))
        code, _ = self.run_cli("--root", "/nonexistent/dir/for/this/test")
        self.assertEqual(2, code)


if __name__ == "__main__":
    unittest.main()
