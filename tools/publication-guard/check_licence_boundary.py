#!/usr/bin/env python3
"""Assert the licence map: every path resolves through REUSE.toml, the Apache
directories hold nothing but Apache-2.0, and a file's own SPDX header never
disagrees with the map.

The repository mixes two licences by directory. People and assistants read
the tree as one thing, so the boundary needs a check rather than a convention:
code may move from the Apache directories into the FCL ones, never the other
way, and the mirror (an export of the Apache directories) must contain nothing
that is not Apache-2.0.

Usage:
    check_licence_boundary.py [--root DIR] [--reuse REUSE.toml]
                              [--apache-dir DIR ...] [--files FILE ...]

Exit 0 when the map holds, 1 on a violation, 2 on a malformed map or usage.
"""

from __future__ import annotations

import argparse
import fnmatch
import re
import subprocess
import sys
import tomllib
from dataclasses import dataclass
from pathlib import Path

APACHE = "Apache-2.0"
DEFAULT_APACHE_DIRS = ("agent/", "proto/", "tools/")
SPDX_HEADER = re.compile(r"SPDX-License-Identifier:\s*([A-Za-z0-9.+-]+)")
HEADER_WINDOW = 4096
SKIP_DIRS = {".git", "node_modules", "dist", "LICENSES"}


@dataclass
class Annotation:
    paths: list[str]
    licence: str
    line: int


class MapError(Exception):
    pass


def load_map(path: Path) -> list[Annotation]:
    try:
        data = tomllib.loads(path.read_text(encoding="utf-8"))
    except (OSError, tomllib.TOMLDecodeError) as exc:
        raise MapError(f"cannot read {path}: {exc}") from exc
    if data.get("version") != 1:
        raise MapError(f"{path}: unsupported REUSE.toml version {data.get('version')!r}")
    out: list[Annotation] = []
    for index, ann in enumerate(data.get("annotations", []), start=1):
        paths = ann.get("path")
        if isinstance(paths, str):
            paths = [paths]
        licence = ann.get("SPDX-License-Identifier")
        if not paths or not isinstance(paths, list) or not licence:
            raise MapError(f"{path}: annotation {index} needs `path` and `SPDX-License-Identifier`")
        if ann.get("precedence", "closest") != "override":
            raise MapError(
                f"{path}: annotation {index} must use precedence = \"override\" — "
                "the directory decision must not be undoable by a stray header"
            )
        out.append(Annotation(paths=paths, licence=str(licence), line=index))
    if not out:
        raise MapError(f"{path}: no annotations")
    return out


def glob_matches(pattern: str, rel: str) -> bool:
    """REUSE globs: `**` spans directories, `*` does not cross `/`."""
    regex = ""
    i = 0
    while i < len(pattern):
        ch = pattern[i]
        if pattern.startswith("**/", i):
            regex += "(?:.*/)?"
            i += 3
            continue
        if pattern.startswith("**", i):
            regex += ".*"
            i += 2
            continue
        if ch == "*":
            regex += "[^/]*"
        elif ch == "?":
            regex += "[^/]"
        else:
            regex += re.escape(ch)
        i += 1
    return re.fullmatch(regex, rel) is not None


def resolve(annotations: list[Annotation], rel: str) -> str | None:
    """The last matching annotation wins (override precedence, file order)."""
    licence = None
    for ann in annotations:
        if any(glob_matches(p, rel) for p in ann.paths):
            licence = ann.licence
    return licence


def tracked_files(root: Path) -> list[str]:
    proc = subprocess.run(["git", "-C", str(root), "ls-files", "-z"], capture_output=True)
    if proc.returncode == 0 and proc.stdout:
        return [p for p in proc.stdout.decode("utf-8", "replace").split("\0") if p]
    out: list[str] = []
    for path in root.rglob("*"):
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        if path.is_file():
            out.append(path.relative_to(root).as_posix())
    return sorted(out)


def header_licence(path: Path) -> str | None:
    try:
        with path.open("rb") as fh:
            head = fh.read(HEADER_WINDOW)
    except OSError:
        return None
    if b"\0" in head:
        return None
    m = SPDX_HEADER.search(head.decode("utf-8", "replace"))
    return m.group(1) if m else None


def check(root: Path, reuse: Path, apache_dirs: tuple[str, ...], files: list[str] | None) -> list[str]:
    annotations = load_map(reuse)
    rels = files if files is not None else tracked_files(root)
    problems: list[str] = []
    counts: dict[str, int] = {}
    for rel in rels:
        if rel.startswith("LICENSES/") or rel == "REUSE.toml":
            continue
        licence = resolve(annotations, rel)
        if licence is None:
            problems.append(f"{rel}:0:licence-unmapped: no REUSE annotation covers this path")
            continue
        counts[licence] = counts.get(licence, 0) + 1
        # An entry is a directory prefix or, for an island, one file (main()
        # appends "/" to every entry, so a file matches by equality).
        if any(rel.startswith(d) or rel == d.rstrip("/") for d in apache_dirs) and licence != APACHE:
            problems.append(f"{rel}:0:licence-boundary: {licence} inside an Apache-2.0 directory")
        declared = header_licence(root / rel)
        if declared and declared != licence:
            problems.append(f"{rel}:0:licence-header: header says {declared}, the map says {licence}")
    print("licence map: " + ", ".join(f"{k}={v}" for k, v in sorted(counts.items())))
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--root", type=Path, default=Path("."), help="tree to check (default: cwd)")
    parser.add_argument("--reuse", type=Path, help="REUSE.toml (default: <root>/REUSE.toml)")
    parser.add_argument("--apache-dir", action="append", help="directory that must hold only Apache-2.0 "
                        f"(default: {', '.join(DEFAULT_APACHE_DIRS)})")
    parser.add_argument("--files", nargs="*", help="check these root-relative paths instead of every tracked file")
    args = parser.parse_args(argv)
    root = args.root.resolve()
    reuse = args.reuse or root / "REUSE.toml"
    apache_dirs = tuple(d if d.endswith("/") else d + "/" for d in (args.apache_dir or DEFAULT_APACHE_DIRS))
    try:
        problems = check(root, reuse, apache_dirs, args.files)
    except MapError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    for p in problems:
        print(p)
    if problems:
        print(f"{len(problems)} licence problem(s)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
