#!/usr/bin/env python3
"""CI contract: every document a file cites exists in the tree it is cited from.

A tree exported as a subset of a larger one keeps the citations and loses the
documents. `docs/<NAME>.md` in a comment then points at nothing, and a reader
who follows it learns that the published tree is not the whole story. This
check turns that into a failing job at the moment the citation is written or
the document is dropped.

Two citation shapes are recognised:
  - a path: `docs/<NAME>.md`, anywhere in the text;
  - a bare name in the usual shapes of prose: `` `<NAME>.md` `` or `see <NAME>.md`,
    where NAME is upper-case (`NETWORK-FIRST`), resolved against `docs/`,
    the citing file's own directory and the root.

Usage: check_doc_citations.py [--root DIR] [--allow FILE]
Output: `path:line:doc-citation: docs/<NAME>.md does not exist`.
Exit: 0 clean, 1 missing citations, 2 usage error.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path, PurePosixPath
from typing import Iterator, Sequence

PATH_CITATION_RE = re.compile(r"(?<![\w/-])docs/([A-Za-z0-9._-]+?\.md)\b")
BARE_CITATION_RE = re.compile(r"(?:`([A-Z][A-Z0-9-]+\.md)`|\bsee ([A-Z][A-Z0-9-]+\.md)\b)")

SKIP_DIR_PARTS = {"node_modules", "dist", ".git"}
SKIP_BASENAMES = {"pnpm-lock.yaml", "go.sum", "package-lock.json", "yarn.lock"}
SKIP_SUFFIXES = {".pb.go", ".woff2", ".woff", ".png", ".ico", ".jpg", ".jpeg", ".gif", ".pdf"}
BINARY_PROBE_BYTES = 8192


def is_skipped(rel: PurePosixPath) -> bool:
    if any(part in SKIP_DIR_PARTS for part in rel.parts[:-1]):
        return True
    if rel.name in SKIP_BASENAMES:
        return True
    return any(rel.name.endswith(s) for s in SKIP_SUFFIXES)


def walk(root: Path) -> Iterator[PurePosixPath]:
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIR_PARTS)
        for name in sorted(filenames):
            full = Path(dirpath) / name
            if full.is_symlink():
                continue
            rel = PurePosixPath(full.relative_to(root).as_posix())
            if not is_skipped(rel):
                yield rel


def read_allow(path: Path | None) -> set[str]:
    """Citations deliberately left pointing at a private document, one `docs/<NAME>.md` per line."""
    if path is None:
        return set()
    allowed: set[str] = set()
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.split("#", 1)[0].strip()
        if line:
            allowed.add(line if line.startswith("docs/") else "docs/" + line)
    return allowed


def citations_in(line: str, citing: PurePosixPath) -> Iterator[tuple[str, list[str]]]:
    """Yield (display name, candidate relative paths) for every citation on a line."""
    for m in PATH_CITATION_RE.finditer(line):
        name = m.group(1)
        yield f"docs/{name}", [f"docs/{name}"]
    for m in BARE_CITATION_RE.finditer(line):
        name = m.group(1) or m.group(2)
        here = citing.parent.as_posix()
        candidates = [f"docs/{name}", name if here == "." else f"{here}/{name}", name]
        yield f"docs/{name}", candidates


def check(root: Path, allow: set[str]) -> list[str]:
    findings: list[str] = []
    for rel in walk(root):
        full = root / rel
        try:
            data = full.read_bytes()
        except OSError:
            continue
        if b"\x00" in data[:BINARY_PROBE_BYTES]:
            continue
        text = data.decode("utf-8", errors="replace")
        for idx, line in enumerate(text.splitlines(), start=1):
            for display, candidates in citations_in(line, rel):
                if display in allow:
                    continue
                if any((root / c).is_file() for c in candidates):
                    continue
                findings.append(f"{rel}:{idx}:doc-citation: {display} does not exist")
    return findings


def main(argv: Sequence[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--root", default=".", metavar="DIR", help="tree to check (default: cwd)")
    p.add_argument("--allow", metavar="FILE", help="citations allowed to point outside the tree, one per line")
    p.add_argument("--format", choices=("text", "github"), default="text")
    args = p.parse_args(argv)

    root = Path(args.root)
    if not root.is_dir():
        print(f"error: not a directory: {root}", file=sys.stderr)
        return 2
    try:
        allow = read_allow(Path(args.allow) if args.allow else None)
    except OSError as exc:
        print(f"error: cannot read allow file: {exc}", file=sys.stderr)
        return 2

    findings = check(root, allow)
    for f in findings:
        if args.format == "github":
            path, line, rest = f.split(":", 2)
            print(f"::error file={path},line={line},title=doc-citation::{rest.split(': ', 1)[1]}")
        else:
            print(f)
    print(f"doc-citations: {len(findings)} missing", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
