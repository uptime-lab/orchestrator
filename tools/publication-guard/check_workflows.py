#!/usr/bin/env python3
"""Assert that no workflow in a directory can run untrusted code on a private
machine or with secrets it should not have.

A pull request from a fork is evaluated with the fork's own copy of the
workflow files, so the only safe arrangement is structural: every job runs on
a GitHub-hosted runner, nothing triggers on `pull_request_target` or
`workflow_run` (both run with the base repository's secrets), and a self-hosted
label never appears. This check is itself editable by a pull request, which is
why the organisation's runner-group setting and the fork-approval policy are
the boundary and this is the tripwire.

One rule is about coverage rather than trust (guard-path-filter): a workflow
that runs the publication guard declares no path filter, so a docs-only
change is scanned too.

Usage: check_workflows.py DIR_OR_FILE ...   (exit 0 clean, 1 findings, 2 usage)

The runner rule is the repository fork guard's (`backend/pkg/repoguard`): a
job is hosted only if EVERY label is a
GitHub-hosted one or an expression — GitHub routes any other label to
self-hosted runners whether or not `self-hosted` is listed, so
`runs-on: example-arm64` is a self-hosted job. The label set is read from the
scalar, flow-list and block-list forms; the mapping form (`group:`/`labels:`)
cannot be read statically and fails closed. An expression is accepted, but the
JSON-array literals inside it (the `fromJSON(vars.X || '[...]')` fallback) and
the matrix values it names (`${{ matrix.NAME }}`, and `runner:` anywhere) are
labels too. Any OTHER expression (a string literal such as
`${{ 'example-arm64' }}`, format(), another variable) is refused: only
`${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[...]') }}` (the variable may carry
an `_AMD64` or `_ARM64` suffix) and `${{ matrix.NAME }}` are read, and an inline `matrix:` expression fails closed.

Triggers are read from the PARSED document (a small YAML subset reader,
shared byte for byte with tooling outside this tree): `on:` as a scalar, a flow list,
a flow or block mapping, quoted keys included; a file the reader refuses is a
finding. The runner and path-filter rules stay line-based.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any

FORBIDDEN_TRIGGERS = ("pull_request_target", "workflow_run")
# The GitHub-hosted runner labels, as actionlint 1.7.12 knows them (its
# runner-label rule): the -latest and version forms, the arm, larger-runner
# and core-count variants. GitHub routes ANY other label to self-hosted
# runners, whether or not `self-hosted` is listed — `runs-on: example-arm64`
# is a self-hosted job — so a label outside this set makes the job
# self-hosted. Matched case-insensitively, as GitHub matches labels. Keep it
# equal to hostedRunnerRe in backend/pkg/repoguard/workflow_fork_guard_test.go.
HOSTED_RUNNER = re.compile(
    r"^(?:ubuntu-(?:latest|slim|[0-9]{2}\.[0-9]{2})(?:-arm)?(?:-[0-9]+-cores)?"
    r"|windows-(?:latest|[0-9]{4}|11-arm)(?:-vs[0-9]{4}|-[0-9]+-cores)?"
    r"|macos-(?:latest|[0-9]{2})(?:-large|-xlarge|-xl)?)$",
    re.IGNORECASE,
)
# The statement above is shared byte for byte with maintainers' tooling outside
# this tree, which compares the two copies: change it only together with that
# copy. This module imports nothing outside the tree.
KEY_LINE = re.compile(r"""^(\s*)(-\s+)?(['"]?)([A-Za-z0-9_-]+)\3\s*:(?:\s+(.*?))?\s*$""")
BLOCK_ITEM = re.compile(r"^(\s*)-\s+(.*?)\s*$")
MATRIX_REF = re.compile(r"\bmatrix\.([A-Za-z0-9_-]+)")
JSON_LITERAL = re.compile(r"'(\[[^']*\])'")
EXPRESSION = re.compile(r"^\$\{\{.*\}\}$", re.DOTALL)
# The only expressions a runner label may be: the PUBLIC_CI_RUNS_ON variable
# with a literal JSON fallback (every fallback label hosted), or a bare matrix
# reference whose values the matrix rule reads. Anything else — a string
# literal (`${{ 'example-arm64' }}`), format(), another variable, an `||`
# after fromJSON — can name a self-hosted machine. PUBLIC_RUNS_ON is kept equal
# to publicRunsOnRe in the Go test.
PUBLIC_RUNS_ON = re.compile(
    r"^\$\{\{\s*fromJSON\(\s*vars\.PUBLIC_CI_RUNS_ON(?:_AMD64|_ARM64)?\s*\|\|\s*'(\[[^']*\])'\s*\)\s*\}\}$"
)
MATRIX_ONLY = re.compile(r"^\$\{\{\s*matrix\.[A-Za-z0-9_-]+\s*\}\}$")



# The YAML subset reader, from the BEGIN marker line through the END marker,
# is shared byte for byte with maintainers' tooling outside this tree, which
# compares the two copies: change it only together with that copy. This module
# imports nothing outside the tree.
# Triggers are read from the PARSED `on:`, because a line pattern sees only
# the block-mapping form and misses `on: pull_request_target`,
# `on: [push, workflow_run]` and a quoted key.
# >>> BEGIN YAML subset reader (byte-identical copy in tools/publication-guard/check_workflows.py; ci_copy_test.py) >>>
class YAMLSubsetError(ValueError):
    pass


# ---------------------------------------------------------------------------
# The YAML subset reader. Values are dict (insertion-ordered), list or str;
# an empty value is None. No type conversion: `true` stays the string "true",
# and `on` stays the key "on" (YAML 1.1 would make it a boolean).
# ---------------------------------------------------------------------------


def _strip_comment(text: str) -> str:
    """Drop a `#` comment that starts the text or follows whitespace, outside
    quotes. Brackets do not matter here: a `#` inside a flow collection is a
    comment in YAML too when whitespace precedes it."""
    quote = ""
    i = 0
    while i < len(text):
        c = text[i]
        if quote:
            if quote == "'" and c == "'":
                if i + 1 < len(text) and text[i + 1] == "'":
                    i += 2
                    continue
                quote = ""
            elif quote == '"':
                if c == "\\":
                    i += 2
                    continue
                if c == '"':
                    quote = ""
        elif c in "'\"" and (i == 0 or text[i - 1] in " \t[{,:-"):
            quote = c
        elif c == "#" and (i == 0 or text[i - 1] in " \t"):
            return text[:i].rstrip()
        i += 1
    return text.rstrip()


_ESCAPES = {"n": "\n", "t": "\t", "\\": "\\", '"': '"', "/": "/", "0": "\0", "r": "\r", " ": " "}


def _double_quoted(text: str, start: int) -> tuple[str, int]:
    out = []
    i = start + 1
    while i < len(text):
        c = text[i]
        if c == "\\":
            if i + 1 >= len(text):
                break
            e = text[i + 1]
            if e in _ESCAPES:
                out.append(_ESCAPES[e])
                i += 2
                continue
            if e == "u" and re.fullmatch(r"[0-9a-fA-F]{4}", text[i + 2 : i + 6] or ""):
                out.append(chr(int(text[i + 2 : i + 6], 16)))
                i += 6
                continue
            raise YAMLSubsetError(f"unsupported escape \\{e}")
        if c == '"':
            return "".join(out), i + 1
        out.append(c)
        i += 1
    raise YAMLSubsetError("unterminated double-quoted scalar")


def _single_quoted(text: str, start: int) -> tuple[str, int]:
    out = []
    i = start + 1
    while i < len(text):
        c = text[i]
        if c == "'":
            if i + 1 < len(text) and text[i + 1] == "'":
                out.append("'")
                i += 2
                continue
            return "".join(out), i + 1
        out.append(c)
        i += 1
    raise YAMLSubsetError("unterminated single-quoted scalar")


def _check_plain(value: str) -> str:
    if value[:1] in ("&", "*", "!"):
        raise YAMLSubsetError(f"anchors, aliases and tags are not supported: {value!r}")
    if value[:1] in ("%", "@", "`") or value[:2] in ("? ", ": ") or value in ("?", ":"):
        raise YAMLSubsetError(f"a plain scalar cannot start with {value[0]!r}")
    return value


class _Flow:
    """Recursive descent over one flow collection (`[...]` / `{...}`)."""

    def __init__(self, text: str) -> None:
        self.text = text
        self.i = 0

    def ws(self) -> None:
        while self.i < len(self.text) and self.text[self.i] in " \t\n":
            self.i += 1

    def node(self) -> Any:
        self.ws()
        if self.i >= len(self.text):
            raise YAMLSubsetError("unexpected end of a flow collection")
        c = self.text[self.i]
        if c == "[":
            return self.seq()
        if c == "{":
            return self.mapping()
        return self.scalar()

    def scalar(self) -> Any:
        c = self.text[self.i]
        if c == '"':
            value, self.i = _double_quoted(self.text, self.i)
            return value
        if c == "'":
            value, self.i = _single_quoted(self.text, self.i)
            return value
        start = self.i
        while self.i < len(self.text):
            c = self.text[self.i]
            if c in ",]}":
                break
            if c == ":" and (self.i + 1 >= len(self.text) or self.text[self.i + 1] in " \t\n,]}"):
                break
            self.i += 1
        value = self.text[start : self.i].strip()
        return _check_plain(value) if value else None

    def seq(self) -> list:
        self.i += 1
        out: list = []
        while True:
            self.ws()
            if self.i >= len(self.text):
                raise YAMLSubsetError("unterminated flow sequence")
            if self.text[self.i] == "]":
                self.i += 1
                return out
            out.append(self.node())
            self.ws()
            if self.i < len(self.text) and self.text[self.i] == ",":
                self.i += 1
            elif self.i < len(self.text) and self.text[self.i] != "]":
                raise YAMLSubsetError(f"unexpected {self.text[self.i]!r} in a flow sequence")

    def mapping(self) -> dict:
        self.i += 1
        out: dict = {}
        while True:
            self.ws()
            if self.i >= len(self.text):
                raise YAMLSubsetError("unterminated flow mapping")
            if self.text[self.i] == "}":
                self.i += 1
                return out
            key = self.scalar()
            if not isinstance(key, str):
                raise YAMLSubsetError("a flow mapping key must be a scalar")
            self.ws()
            value = None
            if self.i < len(self.text) and self.text[self.i] == ":":
                self.i += 1
                self.ws()
                if self.i < len(self.text) and self.text[self.i] not in ",}":
                    value = self.node()
            if key in out:
                raise YAMLSubsetError(f"duplicate key {key!r}")
            out[key] = value
            self.ws()
            if self.i < len(self.text) and self.text[self.i] == ",":
                self.i += 1
            elif self.i < len(self.text) and self.text[self.i] != "}":
                raise YAMLSubsetError(f"unexpected {self.text[self.i]!r} in a flow mapping")


def _flow_balanced(text: str) -> bool:
    depth = 0
    quote = ""
    i = 0
    while i < len(text):
        c = text[i]
        if quote:
            if c == "\\" and quote == '"':
                i += 2
                continue
            if c == quote:
                quote = ""
        elif c in "'\"":
            quote = c
        elif c in "[{":
            depth += 1
        elif c in "]}":
            depth -= 1
        i += 1
    return depth <= 0 and not quote


def _split_key(content: str) -> tuple[str, str] | None:
    """`key: rest` -> (key, rest); None when the line is not a mapping entry."""
    if content.startswith(("? ", "- ")) or content in ("?", "-"):
        return None
    if content[:1] in ("[", "{"):
        return None
    if content[:1] in ('"', "'"):
        try:
            key, end = (_double_quoted if content[0] == '"' else _single_quoted)(content, 0)
        except YAMLSubsetError:
            return None
        rest = content[end:].lstrip(" \t")
        if not rest.startswith(":") or (len(rest) > 1 and rest[1] not in " \t"):
            return None
        return key, rest[1:].strip()
    m = re.search(r":(\s|$)", content)
    if not m:
        return None
    key = content[: m.start()].strip()
    if not key:
        return None
    return _check_plain(key), content[m.end() :].strip()


class _Block:
    def __init__(self, text: str) -> None:
        self.raw = text.splitlines()
        # (indent, content-without-comment) per line; None for blank/comment.
        # Computed for every line, but only a line read as STRUCTURE is held
        # to the structural rules (`_structural`): a block scalar's content
        # (a `run: |` script) may hold tabs, `---` or anything else.
        self.lines: list[tuple[int, str] | None] = []
        for raw in self.raw:
            content = _strip_comment(raw.strip())
            self.lines.append((len(raw) - len(raw.lstrip(" ")), content) if content else None)

    def _structural(self, i: int) -> tuple[int, str]:
        raw = self.raw[i]
        if "\t" in raw[: len(raw) - len(raw.lstrip())]:
            raise YAMLSubsetError(f"line {i + 1}: a tab in indentation")
        content = self.lines[i][1]
        if content in ("---", "...") or content.startswith(("--- ", "%")):
            raise YAMLSubsetError(f"line {i + 1}: document markers and directives are not supported")
        return self.lines[i]

    def next_content(self, i: int) -> int:
        while i < len(self.lines) and self.lines[i] is None:
            i += 1
        return i

    def document(self) -> Any:
        i = self.next_content(0)
        if i >= len(self.lines):
            return None
        value, i = self.node(i, self._structural(i)[0])
        i = self.next_content(i)
        if i < len(self.lines):
            raise YAMLSubsetError(f"line {i + 1}: unexpected content after the document")
        return value

    def node(self, i: int, indent: int) -> tuple[Any, int]:
        ind, content = self._structural(i)
        if ind != indent:
            raise YAMLSubsetError(f"line {i + 1}: unexpected indentation")
        if content == "-" or content.startswith("- "):
            return self.sequence(i, indent)
        if _split_key(content) is not None:
            return self.mapping(i, indent)
        return self.inline(i, indent - 1, content)

    def sequence(self, i: int, indent: int) -> tuple[list, int]:
        out: list = []
        while True:
            i = self.next_content(i)
            if i >= len(self.lines):
                return out, i
            ind, content = self._structural(i)
            if ind < indent:
                return out, i
            if ind > indent or not (content == "-" or content.startswith("- ")):
                raise YAMLSubsetError(f"line {i + 1}: expected a sequence item")
            rest = content[1:].lstrip(" ")
            if not rest:
                j = self.next_content(i + 1)
                if j < len(self.lines) and self.lines[j][0] > indent:
                    value, i = self.node(j, self.lines[j][0])
                else:
                    value, i = None, i + 1
                out.append(value)
                continue
            # `- key: value` opens a mapping whose keys sit at the column of
            # `key`; `- - x` a nested sequence. Re-read the line at that column.
            column = indent + (len(content) - len(rest))
            if rest == "-" or rest.startswith("- ") or _split_key(rest) is not None:
                self.lines[i] = (column, rest)
                value, i = self.node(i, column)
            else:
                value, i = self.inline(i, indent, rest)
            out.append(value)

    def mapping(self, i: int, indent: int) -> tuple[dict, int]:
        out: dict = {}
        while True:
            i = self.next_content(i)
            if i >= len(self.lines):
                return out, i
            ind, content = self._structural(i)
            if ind < indent:
                return out, i
            split = _split_key(content) if ind == indent else None
            if split is None:
                raise YAMLSubsetError(f"line {i + 1}: expected `key: value` at column {indent}")
            key, rest = split
            if key in out:
                raise YAMLSubsetError(f"line {i + 1}: duplicate key {key!r}")
            if rest:
                value, i = self.inline(i, indent, rest)
            else:
                j = self.next_content(i + 1)
                if j < len(self.lines) and self.lines[j][0] > indent:
                    value, i = self.node(j, self.lines[j][0])
                elif j < len(self.lines) and self.lines[j][0] == indent and (
                    self.lines[j][1] == "-" or self.lines[j][1].startswith("- ")
                ):
                    value, i = self.sequence(j, indent)  # `key:` then `- item` at the same column
                else:
                    value, i = None, i + 1
            out[key] = value

    def inline(self, i: int, indent: int, rest: str) -> tuple[Any, int]:
        """The value on the line of its key or dash. `indent` is the column of
        the owning key or dash: continuation lines must be deeper."""
        if rest[:1] in ("|", ">"):
            return self.block_scalar(i, indent, rest)
        if rest[:1] in ("[", "{"):
            text = rest
            j = i + 1
            while not _flow_balanced(text):
                if j >= len(self.lines):
                    raise YAMLSubsetError(f"line {i + 1}: unterminated flow collection")
                if self.lines[j] is not None:
                    text += "\n" + self.lines[j][1]
                j += 1
            parser = _Flow(text)
            value = parser.node()
            parser.ws()
            if parser.i != len(text):
                raise YAMLSubsetError(f"line {i + 1}: unexpected text after a flow collection")
            return value, j
        if rest[:1] in ('"', "'"):
            text = rest
            j = i + 1
            # A quoted scalar may continue on following lines; fold them.
            while True:
                try:
                    value, end = (_double_quoted if text[0] == '"' else _single_quoted)(text, 0)
                    break
                except YAMLSubsetError as exc:
                    if "unterminated" not in str(exc) or j >= len(self.raw):
                        raise YAMLSubsetError(f"line {i + 1}: {exc}") from None
                    text += " " + self.raw[j].strip()
                    j += 1
            if text[end:].strip():
                raise YAMLSubsetError(f"line {i + 1}: unexpected text after a quoted scalar")
            return value, j
        # A plain scalar, possibly continued on deeper lines (folded with spaces).
        parts = [_check_plain(rest)]
        j = i + 1
        while True:
            k = self.next_content(j)
            if k >= len(self.lines) or self.lines[k][0] <= indent:
                break
            parts.append(self.lines[k][1])
            j = k + 1
        for part in parts:
            # `a: b: c`, or a deeper `key: value` under a scalar: YAML refuses
            # both ("mapping values are not allowed here"); so does this.
            if re.search(r":(\s|$)", part):
                raise YAMLSubsetError(f"line {i + 1}: `: ` inside a plain scalar")
        return " ".join(parts), j

    def block_scalar(self, i: int, indent: int, header: str) -> tuple[str, int]:
        m = re.fullmatch(r"([|>])([+-]?)([1-9]?)([+-]?)", header)
        if not m:
            raise YAMLSubsetError(f"line {i + 1}: unsupported block scalar header {header!r}")
        style, chomp = m.group(1), (m.group(2) or m.group(4))
        body: list[str] = []
        j = i + 1
        block_indent = None
        while j < len(self.raw):
            raw = self.raw[j]
            if raw.strip():
                ind = len(raw) - len(raw.lstrip(" "))
                if ind <= indent:
                    break
                if block_indent is None:
                    block_indent = int(m.group(3)) + indent if m.group(3) else ind
                if ind < block_indent:
                    break
                body.append(raw[block_indent:])
            else:
                body.append("")
            j += 1
        while body and body[-1] == "":
            body.pop()
        if style == "|":
            text = "\n".join(body)
        else:
            # Folded: a line break between two non-empty lines becomes a
            # space, an empty line a newline. (More-indented lines keep their
            # breaks in YAML; no workflow `if:` uses them, and a `run:` value
            # is never judged here.)
            text = ""
            for n, line in enumerate(body):
                if n == 0:
                    text = line
                elif line == "":
                    text += "\n"
                elif body[n - 1] == "":
                    text += line
                else:
                    text += " " + line
        if chomp != "-" and body:
            text += "\n"
        return text, j


def load(text: str) -> Any:
    return _Block(text).document()
# <<< END YAML subset reader <<<

# The guard jobs must run on EVERY change: prose is where a serial, an address
# or a tenant name lands, and a path filter skips exactly the docs-only change.
# A workflow carrying either job therefore declares no path filter at all.
GUARD_JOBS = ("publication-guard", "private-identifier-guard")
TOP_KEY = re.compile(r"""^(['"]?)([A-Za-z_][A-Za-z0-9_-]*)\1\s*:""")
CHILD_KEY = re.compile(r"""^(\s+)(['"]?)([A-Za-z0-9_-]+)\2\s*:""")
PATH_FILTER = re.compile(r"""^\s+(['"]?)(paths|paths-ignore)\1\s*:""")


def check_guard_path_filter(name: str, text: str) -> list[str]:
    """guard-path-filter: a workflow whose jobs include a guard job declares
    no `paths`/`paths-ignore` under its `on:` triggers."""
    section = ""
    jobs_indent: int | None = None
    guard_jobs: list[str] = []
    filters: list[tuple[int, str]] = []
    for number, line in enumerate(text.splitlines(), start=1):
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        top = TOP_KEY.match(line)
        if top:
            section = top.group(2)
            jobs_indent = None
            continue
        if section == "on":
            m = PATH_FILTER.match(line)
            if m:
                filters.append((number, m.group(2)))
        elif section == "jobs":
            child = CHILD_KEY.match(line)
            if child:
                indent = len(child.group(1))
                if jobs_indent is None:
                    jobs_indent = indent
                if indent == jobs_indent and child.group(3) in GUARD_JOBS:
                    guard_jobs.append(child.group(3))
    if not guard_jobs:
        return []
    return [
        f"{name}:{number}:guard-path-filter: `{key}` on a workflow that runs {', '.join(guard_jobs)};"
        " the guard must run on every change, docs-only ones included"
        for number, key in filters
    ]


def _unquote(value: str) -> str:
    return value.strip().strip("'\"")


def non_hosted(label: str) -> list[str]:
    """The labels in `label` that are not GitHub-hosted. An expression is
    accepted only in two exact shapes: the PUBLIC_CI_RUNS_ON fallback form,
    whose JSON list is a label list read like any other (it is what the job
    runs on whenever the variable is unset), and `${{ matrix.NAME }}`, whose
    values the matrix rule reads. Any other expression is refused whole."""
    label = _unquote(label)
    if not EXPRESSION.match(label):
        return [] if HOSTED_RUNNER.match(label) else [label]
    if MATRIX_ONLY.match(label):
        return []
    m = PUBLIC_RUNS_ON.match(label)
    if not m:
        return [label]
    try:
        items = json.loads(m.group(1))
    except ValueError:
        return [m.group(1)]
    if not isinstance(items, list) or not items:
        return [m.group(1)]
    return [str(i) for i in items if not isinstance(i, str) or not HOSTED_RUNNER.match(i)]


def _code_lines(text: str) -> list[tuple[int, str]]:
    out = []
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.split("#", 1)[0] if not line.lstrip().startswith("#") else ""
        if stripped.strip():
            out.append((number, stripped.rstrip()))
    return out


def _indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def runner_values(lines: list[tuple[int, str]], i: int, key_col: int, value: str) -> tuple[list[str], bool]:
    """The labels a `runs-on:`/matrix key at lines[i] names, and whether it is
    the mapping form, which cannot be read statically (fail closed)."""
    if value:
        if value.startswith("{"):
            return [], True
        if value.startswith("["):
            return [v for v in (_unquote(x) for x in value.strip("[]").split(",")) if v], False
        return [value], False
    labels: list[str] = []
    mapping = False
    for _, line in lines[i + 1:]:
        if _indent(line) <= key_col:
            break
        item = BLOCK_ITEM.match(line)
        if item:
            labels.append(_unquote(item.group(2)))
        elif KEY_LINE.match(line):
            mapping = True
    return labels, mapping


def check_runners(name: str, lines: list[tuple[int, str]]) -> list[str]:
    """workflow-runner: every label a job can run on is GitHub-hosted (or an
    expression whose literals and matrix values are)."""
    matrix_names = {"runner"}
    for _, line in lines:
        m = KEY_LINE.match(line)
        if m and m.group(4) == "runs-on" and m.group(5):
            matrix_names.update(MATRIX_REF.findall(m.group(5)))
    out: list[str] = []
    matrix_col: int | None = None
    for i, (number, line) in enumerate(lines):
        m = KEY_LINE.match(line)
        if matrix_col is not None and _indent(line) <= matrix_col:
            matrix_col = None
        if not m:
            continue
        key_col = len(m.group(1)) + len(m.group(2) or "")
        key, value = m.group(4), (m.group(5) or "").strip()
        if key == "matrix":
            if value:
                # `matrix: ${{ fromJSON(...) }}` (or any inline value): its
                # labels cannot be read statically, so a `${{ matrix.X }}`
                # runner would go unchecked. Fail closed.
                out.append(
                    f"{name}:{number}:workflow-runner: an inline `matrix:` value cannot be read statically;"
                    " a matrix that names runners is written out"
                )
                continue
            matrix_col = key_col
            continue
        # `runner:` is a label anywhere, as it always was; any other name only
        # inside a matrix, where `${{ matrix.NAME }}` reads it.
        if key != "runs-on" and not (key == "runner" or (key in matrix_names and matrix_col is not None)):
            continue
        labels, mapping = runner_values(lines, i, key_col, value)
        if mapping:
            out.append(
                f"{name}:{number}:workflow-runner: the mapping form of `{key}:` (`group:`/`labels:`) cannot be read"
                " statically; a public job runs on a GitHub-hosted image name or an expression"
            )
        for label in labels:
            for bad in non_hosted(label):
                out.append(f"{name}:{number}:workflow-runner: `{bad}` is not a GitHub-hosted runner name")
    return out


def _triggers(on: Any) -> list[str]:
    if isinstance(on, str):
        return [on]
    if isinstance(on, list):
        return [str(x) for x in on]
    if isinstance(on, dict):
        return [str(k) for k in on.keys()]
    raise YAMLSubsetError("unexpected shape for `on:`")


def check_triggers(name: str, text: str) -> list[str]:
    """workflow-trigger, from the parsed document in every shape `on:` takes;
    a file the subset reader refuses is a finding (workflow-parse): fail
    closed rather than pass a file nobody read."""
    try:
        doc = load(text)
        if not isinstance(doc, dict):
            raise YAMLSubsetError("the document is not a mapping")
        events = _triggers(doc.get("on")) if doc.get("on") is not None else []
    except YAMLSubsetError as exc:
        return [f"{name}:1:workflow-parse: {exc} — a workflow this check cannot read is refused"]
    return [
        f"{name}:1:workflow-trigger: `{ev}` runs with the base repository's secrets"
        for ev in FORBIDDEN_TRIGGERS
        if ev in events
    ]


def check_text(name: str, text: str) -> list[str]:
    out: list[str] = check_guard_path_filter(name, text)
    out.extend(check_triggers(name, text))
    lines = _code_lines(text)
    for number, stripped in lines:
        if "self-hosted" in stripped:
            out.append(f"{name}:{number}:workflow-runner: a self-hosted label")
    out.extend(check_runners(name, lines))
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("paths", nargs="+", help="workflow files or directories of them")
    args = parser.parse_args(argv)
    files: list[Path] = []
    for p in args.paths:
        path = Path(p)
        if path.is_dir():
            files.extend(sorted(x for x in path.iterdir() if x.suffix in (".yml", ".yaml")))
        elif path.is_file():
            files.append(path)
        else:
            print(f"error: {p} is neither a file nor a directory", file=sys.stderr)
            return 2
    if not files:
        print("error: no workflow files found", file=sys.stderr)
        return 2
    findings: list[str] = []
    for f in files:
        findings.extend(check_text(str(f), f.read_text(encoding="utf-8", errors="replace")))
    for line in findings:
        print(line)
    print(f"workflows: {len(files)} checked, {len(findings)} finding(s)")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
