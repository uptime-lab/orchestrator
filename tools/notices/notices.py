#!/usr/bin/env python3
"""Generate the third-party notices from what is actually compiled and bundled.

MIT, BSD and ISC require their copyright and permission notice to travel with
a binary redistribution, and Apache-2.0 section 4(d) requires a dependency's
NOTICE file to. The server image, the agent package and the web UI bundle are
binary redistributions, so the notices are generated from their real inputs
rather than kept by hand:

* Go: `go list -deps` over each shipped binary's packages (pinned GOOS/GOARCH,
  so the answer does not depend on the machine), then every LICENSE*,
  LICENCE*, COPYING* and NOTICE* file at the root of each module in the module
  cache, plus the Go standard library's own LICENSE.
* npm: the closure of `frontend/package.json` `dependencies` through pnpm's
  `node_modules` layout (dependencies only: dev, optional and peer edges are
  not walked, so a full and a production-only install give the same answer),
  with each package's licence files and its declared `license` field.

Identical texts are printed once and referenced from every component that
carries them. A text under a copyleft licence fails the run: the licence map
of this repository promises that nothing compiled into a shipped binary needs
one.

Outputs (all regenerated together):
  THIRD-PARTY-NOTICES.md                 every shipped binary and the web UI
  agent/THIRD-PARTY-NOTICES.md           the agent and watcher packages
  mirror-root/THIRD-PARTY-NOTICES.md     the same text, at the mirror's root;
                                         written and checked only when a
                                         mirror-root/ directory exists

Usage:
  notices.py [--root DIR]            write the outputs
  notices.py [--root DIR] --check    exit 1 when an output is stale

Exit 0 clean, 1 stale output or a refused licence, 2 when an input could not be
read (no Go toolchain, no installed node_modules): a check that could not see
its input is not a clean check.
"""

from __future__ import annotations

import argparse
import difflib
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

GENERATOR = "tools/notices/notices.py"

LICENCE_FILE = re.compile(r"^(LICEN[CS]E|COPYING|NOTICE|COPYRIGHT|UNLICENSE)([._-][A-Za-z0-9._-]*)?$", re.IGNORECASE)
NOT_A_LICENCE_SUFFIX = (".go", ".js", ".cjs", ".mjs", ".ts", ".d.ts", ".json", ".html", ".py", ".sh")

# Words that mark a text this repository may not compile into a shipped binary.
COPYLEFT_MARKERS = (
    "GNU GENERAL PUBLIC LICENSE",
    "GNU LESSER GENERAL PUBLIC LICENSE",
    "GNU LIBRARY GENERAL PUBLIC LICENSE",
    "GNU AFFERO GENERAL PUBLIC LICENSE",
    "Server Side Public License",
    "Business Source License",
)
PERMISSIVE_SPDX = {
    "MIT", "MIT-0", "ISC", "0BSD", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0",
    "Unlicense", "CC0-1.0", "BlueOak-1.0.0", "Python-2.0", "Zlib",
}


@dataclass(frozen=True)
class GoTarget:
    key: str
    title: str
    module_dir: str
    packages: tuple[str, ...]
    goos: str
    goarch: str
    cgo: str


GO_TARGETS = (
    GoTarget("server", "Server (`backend/cmd/server`, the backend image)", "backend",
             ("./cmd/server",), "linux", "amd64", "0"),
    GoTarget("agent", "Agent (`agent/cmd/agent`, `agent/cmd/watcher`, the agent and watcher packages)", "agent",
             ("./cmd/agent", "./cmd/watcher"), "linux", "arm64", "0"),
    GoTarget("agent-signer", "Artifact signer (`tools/agent-signer`)", "tools/agent-signer",
             (".",), "linux", "amd64", "1"),
    GoTarget("entitlement-signer", "Entitlement signer (`tools/entitlement-signer`)", "tools/entitlement-signer",
             (".",), "linux", "amd64", "0"),
)
NPM_TITLE = "Web UI (`frontend/`, production dependencies of the bundle)"


class InputError(Exception):
    """An input could not be read; the run cannot answer."""


class RefusedLicence(Exception):
    """A component carries a licence this repository may not ship."""


@dataclass
class Component:
    name: str
    version: str
    licence: str
    texts: list[tuple[str, str]] = field(default_factory=list)  # (file name, normalized text)
    note: str = ""


@dataclass
class Section:
    title: str
    components: list[Component]


# --- text handling ---------------------------------------------------------------

def normalize(raw: bytes) -> str:
    text = raw.decode("utf-8", "replace").replace("\r\n", "\n").replace("\r", "\n")
    lines = [line.rstrip() for line in text.split("\n")]
    while lines and not lines[0]:
        lines.pop(0)
    while lines and not lines[-1]:
        lines.pop()
    return "\n".join(lines)


def classify(text: str) -> str:
    flat = " ".join(text.split())
    if any(m.lower() in flat.lower() for m in COPYLEFT_MARKERS):
        return "copyleft"
    if "Apache License" in flat and "Version 2.0" in flat:
        return "Apache-2.0"
    if "Permission is hereby granted, free of charge" in flat:
        return "MIT"
    if "Redistribution and use in source and binary forms" in flat:
        if "endorse or promote" in flat or "Neither the name" in flat:
            return "BSD-3-Clause"
        return "BSD-2-Clause"
    if "Permission to use, copy, modify, and/or distribute this software" in flat or \
            "Permission to use, copy, modify, and distribute this software" in flat:
        return "ISC" if "provided that the above copyright notice" in flat else "0BSD"
    if "free and unencumbered software released into the public domain" in flat:
        return "Unlicense"
    if "Mozilla Public License" in flat:
        return "MPL-2.0"
    return "other"


def licence_files(directory: Path) -> list[tuple[str, str]]:
    out: list[tuple[str, str]] = []
    try:
        entries = sorted(directory.iterdir(), key=lambda p: p.name)
    except OSError as exc:
        raise InputError(f"cannot list {directory}: {exc}") from exc
    for entry in entries:
        if not entry.is_file() or not LICENCE_FILE.match(entry.name):
            continue
        if entry.name.lower().endswith(NOT_A_LICENCE_SUFFIX):
            continue
        text = normalize(entry.read_bytes())
        if text:
            out.append((entry.name, text))
    return out


def summarize(texts: list[tuple[str, str]]) -> str:
    kinds: list[str] = []
    for name, text in texts:
        kind = "NOTICE" if name.upper().startswith("NOTICE") else classify(text)
        if kind not in kinds:
            kinds.append(kind)
    return " + ".join(kinds) if kinds else "none"


# --- Go -------------------------------------------------------------------------

def run(cmd: list[str], cwd: Path, env: dict[str, str] | None = None) -> str:
    try:
        proc = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, env=env)
    except OSError as exc:
        raise InputError(f"{' '.join(cmd)}: {exc}") from exc
    if proc.returncode != 0:
        raise InputError(f"{' '.join(cmd)} in {cwd} failed:\n{proc.stderr.strip()}")
    return proc.stdout


def go_toolchain(module_dir: Path) -> str:
    """The toolchain go.mod pins (the `toolchain` line, else the `go` line)."""
    gomod = (module_dir / "go.mod").read_text(encoding="utf-8")
    m = re.search(r"^toolchain\s+(\S+)", gomod, re.MULTILINE)
    if m:
        return m.group(1)
    m = re.search(r"^go\s+(\S+)", gomod, re.MULTILINE)
    return f"go{m.group(1)}" if m else "go"


def go_section(root: Path, target: GoTarget) -> Section:
    module_dir = root / target.module_dir
    env = dict(os.environ, GOOS=target.goos, GOARCH=target.goarch, CGO_ENABLED=target.cgo, GOFLAGS="-mod=readonly")
    fmt = "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}" \
          "{{with .Replace}}\t{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}{{end}}"
    listing = run(["go", "list", "-deps", "-f", fmt, *target.packages], module_dir, env)
    modules: dict[str, tuple[str, str]] = {}
    for line in listing.splitlines():
        if not line.strip():
            continue
        parts = line.split("\t")
        path, version, directory = parts[0], parts[1], parts[2]
        if len(parts) >= 6:  # a replace directive: the replacement is what compiled
            version = parts[4] or f"{version} (replaced by {parts[3]})"
            directory = parts[5]
        if not directory:
            raise InputError(f"{path}@{version}: not in the module cache (run `go mod download` in {target.module_dir})")
        modules[path] = (version, directory)
    goroot = run(["go", "env", "GOROOT"], module_dir, env).strip()
    std_texts = licence_files(Path(goroot))
    std_texts = [t for t in std_texts if t[0] == "LICENSE"]
    if not std_texts:
        raise InputError(f"no LICENSE in GOROOT {goroot}")
    components = [Component("Go standard library and runtime", go_toolchain(module_dir), summarize(std_texts), std_texts)]
    for path in sorted(modules):
        version, directory = modules[path]
        texts = licence_files(Path(directory))
        if not texts:
            raise InputError(f"{path}@{version}: no licence file at the module root ({directory})")
        components.append(Component(path, version, summarize(texts), texts))
    return Section(target.title, components)


# --- npm ------------------------------------------------------------------------

def spdx_permits(expression: str) -> bool:
    """True when the SPDX expression offers at least one permissive choice."""
    expr = expression.strip().strip("()")
    for alternative in re.split(r"\s+OR\s+", expr):
        ids = [t for t in re.split(r"[\s()]+|AND", alternative) if t]
        if ids and all(i.strip() in PERMISSIVE_SPDX for i in ids):
            return True
    return False


def declared_licence(meta: dict) -> str:
    lic = meta.get("license")
    if isinstance(lic, dict):
        lic = lic.get("type")
    if not lic and isinstance(meta.get("licenses"), list):
        lic = " OR ".join(str(x.get("type", "")) for x in meta["licenses"] if isinstance(x, dict))
    return str(lic or "UNDECLARED")


def package_dir(node_modules: Path, name: str) -> Path:
    return node_modules.joinpath(*name.split("/"))


def containing_node_modules(real: Path, name: str) -> Path:
    depth = len(name.split("/"))
    out = real
    for _ in range(depth):
        out = out.parent
    return out


def npm_section(root: Path) -> Section:
    frontend = root / "frontend"
    top = frontend / "node_modules"
    if not (top / ".pnpm").is_dir():
        raise InputError("frontend/node_modules is not a pnpm install (run `npx pnpm@9 install --frozen-lockfile` in frontend/)")
    manifest = json.loads((frontend / "package.json").read_text(encoding="utf-8"))
    hoisted = top / ".pnpm" / "node_modules"
    seen: dict[tuple[str, str], Component] = {}
    queue: list[tuple[Path, str, str]] = [(top, name, "frontend/package.json") for name in sorted(manifest.get("dependencies", {}))]
    visited: set[Path] = set()
    while queue:
        base, name, parent = queue.pop()
        candidate = package_dir(base, name)
        if not candidate.exists():
            candidate = package_dir(hoisted, name)
        if not candidate.exists():
            raise InputError(f"{name} (needed by {parent}) is not installed")
        real = candidate.resolve()
        if real in visited:
            continue
        visited.add(real)
        meta = json.loads((real / "package.json").read_text(encoding="utf-8"))
        version = str(meta.get("version", ""))
        declared = declared_licence(meta)
        texts = licence_files(real)
        note = ""
        if not texts:
            author = meta.get("author")
            if isinstance(author, dict):
                author = author.get("name")
            note = f"The package ships no licence file; it declares `{declared}`" + (f" and names {author} as author." if author else ".")
        key = (str(meta.get("name", name)), version)
        if key not in seen:
            seen[key] = Component(key[0], version, declared, texts, note)
        deps_base = containing_node_modules(real, name)
        for dep in sorted(meta.get("dependencies", {}) or {}):
            queue.append((deps_base, dep, f"{name}@{version}"))
    components = [seen[k] for k in sorted(seen)]
    return Section(NPM_TITLE, components)


# --- policy -----------------------------------------------------------------------

def enforce_policy(sections: list[Section]) -> None:
    refused: list[str] = []
    for section in sections:
        for comp in section.components:
            texts_copyleft = any(classify(t) == "copyleft" for _, t in comp.texts)
            if comp.licence in ("UNDECLARED",) and not comp.texts:
                refused.append(f"{comp.name}@{comp.version}: no declared licence and no licence file")
            if texts_copyleft and not spdx_permits(comp.licence):
                refused.append(f"{comp.name}@{comp.version}: copyleft licence text ({comp.licence})")
            if section.title == NPM_TITLE and not spdx_permits(comp.licence):
                refused.append(f"{comp.name}@{comp.version}: declared licence `{comp.licence}` is not on the permissive list")
    if refused:
        raise RefusedLicence("\n".join(refused))


# --- rendering ----------------------------------------------------------------------

MIT_PERMISSION = """Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE."""

# Not package dependencies, so no cache can supply them: the icon SVGs in
# frontend/src/shared/assets/icons/ are redrawn after these two sets, and the
# notice is reproduced as insurance whether or not a redraw is a copy. The
# texts are the upstream LICENSE files of the two projects.
ICON_SECTION = Section(
    "Web UI icon designs (`frontend/src/shared/assets/icons/`)",
    [
        Component("Feather icons", "designs", "MIT",
                  [("LICENSE", "The MIT License (MIT)\n\nCopyright (c) 2013-2023 Cole Bemis\n\n" + MIT_PERMISSION)]),
        Component("Heroicons", "designs", "MIT",
                  [("LICENSE", "MIT License\n\nCopyright (c) Tailwind Labs, Inc.\n\n" + MIT_PERMISSION)]),
    ],
)

STATIC_ATTRIBUTIONS = """## Other attributions

- The WebUSB device-boot client in the web UI
  (`frontend/src/shared/lib/rpiboot/`) reimplements the protocol of Raspberry
  Pi Ltd's `usbboot`, which is licensed under the Apache License 2.0.
- The web UI ships the JetBrains Mono fonts under the SIL Open Font License
  1.1; the font's own licence file, with its copyright line, is beside the
  fonts in `frontend/public/fonts/OFL.txt` and in the built `dist/fonts/`.
- The recovery image served to devices is built with Buildroot and carries
  GPL-2.0, GPL-3.0, LGPL-2.1 and Broadcom-licensed components; its notices
  and the written offer for its corresponding source travel inside the image
  itself (`LICENSES/`, `SOURCE-OFFER.txt`), not with this repository.
"""


def fence_for(text: str) -> str:
    fence = "~~~~"
    while fence in text:
        fence += "~"
    return fence


def render(title: str, intro: str, sections: list[Section], trailer: str) -> str:
    text_ids: dict[str, int] = {}
    users: dict[int, list[str]] = {}
    out: list[str] = [f"# {title}", "", intro.strip(), "",
                      f"<!-- Generated by {GENERATOR}; do not edit by hand. "
                      f"`python3 {GENERATOR} --check` fails CI when this file is stale. -->", ""]
    for section in sections:
        out += [f"## {section.title}", "", "| Component | Version | Licence | Texts |", "|---|---|---|---|"]
        for comp in section.components:
            refs = []
            for _, text in comp.texts:
                if text not in text_ids:
                    text_ids[text] = len(text_ids) + 1
                    users[text_ids[text]] = []
                tid = text_ids[text]
                label = f"{comp.name}@{comp.version}"
                if label not in users[tid]:
                    users[tid].append(label)
                refs.append(f"[T{tid}](#t{tid})")
            cell = ", ".join(dict.fromkeys(refs)) or "—"
            out.append(f"| `{comp.name}` | {comp.version} | {comp.licence} | {cell} |")
        notes = [c for c in section.components if c.note]
        if notes:
            out += ["", "Components that ship no licence file of their own:", ""]
            out += [f"- `{c.name}@{c.version}`: {c.note}" for c in notes]
        out.append("")
    if trailer:
        out += [trailer.strip(), ""]
    out += ["## Licence and notice texts", "",
            "Each text is reproduced once, exactly as the component ships it "
            "(trailing whitespace trimmed), and lists every component that carries it.", ""]
    for text, tid in sorted(text_ids.items(), key=lambda kv: kv[1]):
        fence = fence_for(text)
        out += [f"### T{tid}", "", "Carried by: " + ", ".join(f"`{u}`" for u in users[tid]) + ".", "",
                f"{fence}text", text, fence, ""]
    return "\n".join(out).rstrip("\n") + "\n"


ROOT_INTRO = """
The licence and notice texts that the dependencies compiled into this
repository's shipped binaries, and bundled into its web UI, require to be
reproduced. Every component below is under MIT, BSD, ISC, Apache-2.0 or an
equally permissive licence; the generator refuses a copyleft text. The agent
and watcher packages carry their own subset as
`/usr/share/doc/<package>/THIRD-PARTY-NOTICES.md` (generated into
`agent/THIRD-PARTY-NOTICES.md`), and the recovery image carries the same file
for the agent baked into it.
"""

SERVER_INTRO = """
The licence and notice texts that the dependencies compiled into the
Uptime Orchestrator server (`backend/cmd/server`) require to be reproduced;
this file ships inside the server container image at `/app/`. The server
itself is licensed under the Fair Core License 1.0 (ALv2 future licence);
see the repository's `LICENSE`.
"""

UI_INTRO = """
The licence and notice texts that the packages bundled into the Uptime
Orchestrator web UI, and the icon sets its components follow, require to be
reproduced; this file is served by the UI container at
`/THIRD-PARTY-NOTICES.md`. The UI itself is licensed under the Fair Core
License 1.0 (ALv2 future licence); see the repository's `LICENSE`.
"""

AGENT_INTRO = """
The licence and notice texts that the dependencies compiled into the
Uptime Orchestrator agent (`orchestrator-agent`) and watcher
(`orchestrator-watcher`) require to be reproduced. The agent itself is
licensed under the Apache License 2.0 (`LICENSE`); see `NOTICE`.
"""


def build(root: Path) -> dict[str, str]:
    go = {t.key: go_section(root, t) for t in GO_TARGETS}
    npm = npm_section(root)
    every = [go["server"], go["agent"], go["agent-signer"], go["entitlement-signer"], npm, ICON_SECTION]
    enforce_policy(every)
    full = render("Third-party notices", ROOT_INTRO, every, STATIC_ATTRIBUTIONS)
    agent = render("Third-party notices: agent and watcher", AGENT_INTRO, [go["agent"]], "")
    server = render("Third-party notices: server", SERVER_INTRO, [go["server"]], "")
    ui = render("Third-party notices: web UI", UI_INTRO, [npm, ICON_SECTION], STATIC_ATTRIBUTIONS)
    outputs = {
        "THIRD-PARTY-NOTICES.md": full,
        "agent/THIRD-PARTY-NOTICES.md": agent,
        # The container images are binary distributions too: each Dockerfile's
        # build context is its own directory, so the notices live inside it.
        "backend/THIRD-PARTY-NOTICES.md": server,
        "frontend/public/THIRD-PARTY-NOTICES.md": ui,
    }
    if has_mirror_root(root):
        outputs[f"{MIRROR_ROOT}/THIRD-PARTY-NOTICES.md"] = agent
    return outputs


# The directory the agent source mirror's root files are kept in. The mirror's
# copy is written and checked only when this directory exists; without it there
# is no mirror copy to write, and writing one would create a directory nothing
# reads.
MIRROR_ROOT = "mirror-root"


def has_mirror_root(root: Path) -> bool:
    return (root / MIRROR_ROOT).is_dir()


# Files that must stay byte-identical, because one is what the mirror shows and
# the other is what the agent package installs.
TWINS = (("agent/NOTICE", f"{MIRROR_ROOT}/NOTICE"),)


def check_twins(root: Path) -> list[str]:
    problems = []
    if not has_mirror_root(root):
        return problems
    for a, b in TWINS:
        try:
            if (root / a).read_bytes() != (root / b).read_bytes():
                problems.append(f"{a} and {b} differ; they must be the same text")
        except OSError as exc:
            problems.append(f"cannot read {exc.filename}")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2], help="repository root")
    parser.add_argument("--check", action="store_true", help="compare instead of writing; exit 1 when stale")
    args = parser.parse_args(argv)
    root = args.root.resolve()
    try:
        outputs = build(root)
    except InputError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except RefusedLicence as exc:
        print(f"refused licence(s):\n{exc}", file=sys.stderr)
        return 1
    if not args.check:
        for rel, text in outputs.items():
            (root / rel).write_text(text, encoding="utf-8")
            print(f"wrote {rel} ({len(text.encode())} bytes)")
        problems = check_twins(root)
        for p in problems:
            print(f"warning: {p}", file=sys.stderr)
        return 1 if problems else 0
    stale = []
    for rel, text in outputs.items():
        path = root / rel
        current = path.read_text(encoding="utf-8") if path.exists() else ""
        if current != text:
            stale.append(rel)
            diff = difflib.unified_diff(current.splitlines(), text.splitlines(), f"{rel} (committed)", f"{rel} (generated)", lineterm="", n=1)
            print("\n".join(list(diff)[:40]))
    problems = check_twins(root)
    for rel in stale:
        print(f"{rel}: stale; run `python3 {GENERATOR}` and commit the result", file=sys.stderr)
    for p in problems:
        print(p, file=sys.stderr)
    return 1 if stale or problems else 0


if __name__ == "__main__":
    sys.exit(main())
