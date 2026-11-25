#!/usr/bin/env python3
"""Generate docs/CONFIGURATION.md from the backend's configuration loader.

The variable list is read from `ApplyEnvironment` in
`backend/pkg/webapp/application/config/config.go` (every `os.Getenv("NAME")`
and `secretEnv("NAME")` call), the type from how the value is parsed, and the
default from `getDefaultConfig()` plus the Go zero value of the field. The
"what it does" column comes from `descriptions.toml` beside this script: one
line per variable. A variable with no description fails the run, and a
description for a variable the loader no longer reads fails it too, so the
page cannot drift from the code in either direction.

Usage:
  configref.py                 write docs/CONFIGURATION.md
  configref.py --check         exit 1 when the committed page differs
  configref.py --stdout        print the page instead of writing it
  configref.py --repo DIR      the repository root (default: two levels up)

Standard library only (Python 3.11 or newer, for `tomllib`).
"""

from __future__ import annotations

import argparse
import difflib
import re
import sys
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

CONFIG_GO = Path("backend/pkg/webapp/application/config/config.go")
OUTPUT_MD = Path("docs/CONFIGURATION.md")
DESCRIPTIONS = Path(__file__).resolve().parent / "descriptions.toml"

GETENV_RE = re.compile(r'\b(os\.Getenv|secretEnv)\("([A-Z][A-Z0-9_]*)"\)')
ASSIGN_RE = re.compile(r"\bcfg\.([A-Za-z0-9_.]+)\s*=")
STRUCT_RE = re.compile(r"^type\s+(\w+)\s+struct\s*\{", re.MULTILINE)
CONST_RE = re.compile(r"^\s*(\w+)\s*=\s*(.+?)\s*$")
WEAK_MARKER_RE = re.compile(r"var weakSecretMarkers = \[\]string\{(.*?)\}", re.DOTALL)
INSECURE_ENV_RE = re.compile(r"var insecureEnvironments = map\[string\]bool\{(.*?)\}", re.DOTALL)
MIN_SECRET_RE = re.compile(r"minProductionSecretLength\s*=\s*(\d+)")
WEAK_SECRET_CALL_RE = re.compile(r'weakSecretProblem\("([A-Z0-9_]+)"')

DURATION_UNITS = {"time.Nanosecond": 1, "time.Microsecond": 10**3, "time.Millisecond": 10**6,
                  "time.Second": 10**9, "time.Minute": 60 * 10**9, "time.Hour": 3600 * 10**9}


class ConfigRefError(Exception):
    """A condition that must fail the generation (and therefore CI)."""


@dataclass
class Variable:
    name: str
    secret: bool
    kind: str          # string | int | bytes | float | bool | duration | list
    field_path: str | None
    line: int
    default: str = ""


@dataclass
class GoSource:
    text: str
    structs: dict[str, dict[str, str]] = field(default_factory=dict)   # struct -> field -> type
    consts: dict[str, str] = field(default_factory=dict)               # name -> raw expression
    defaults: dict = field(default_factory=dict)                       # nested literal


# --------------------------------------------------------------------------- Go parsing helpers


def _strip_comments(text: str) -> str:
    """Drop line comments; the loader carries no block comments that matter here."""
    out = []
    for line in text.splitlines():
        in_str = False
        i = 0
        cut = len(line)
        while i < len(line):
            c = line[i]
            if c == '"' and (i == 0 or line[i - 1] != "\\"):
                in_str = not in_str
            elif not in_str and line.startswith("//", i):
                cut = i
                break
            i += 1
        out.append(line[:cut].rstrip())
    return "\n".join(out)


def _function_body(text: str, signature: str) -> str:
    """The text of a function body, by brace balance from its opening brace."""
    start = text.find(signature)
    if start < 0:
        raise ConfigRefError(f"function not found in config.go: {signature!r}")
    brace = text.find("{", start)
    depth = 0
    for i in range(brace, len(text)):
        c = text[i]
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return text[brace + 1:i]
    raise ConfigRefError(f"unbalanced braces after {signature!r}")


def _statement_from(text: str, pos: int) -> str:
    """The `if … { … }` block that starts on the line containing pos."""
    line_start = text.rfind("\n", 0, pos) + 1
    brace = text.find("{", pos)
    if brace < 0:
        return text[line_start:text.find("\n", pos)]
    depth = 0
    for i in range(brace, len(text)):
        c = text[i]
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return text[line_start:i + 1]
    return text[line_start:]


def _classify(block: str) -> str:
    if "time.ParseDuration" in block:
        return "duration"
    if "strconv.ParseBool" in block or '== "true"' in block or 'EqualFold(v, "true")' in block:
        return "bool"
    if "strconv.ParseInt" in block:
        return "bytes"
    if "strconv.ParseFloat" in block:
        return "float"
    if "strconv.Atoi" in block:
        return "int"
    if "strings.Split" in block:
        return "list"
    return "string"


def parse_structs(text: str) -> dict[str, dict[str, str]]:
    structs: dict[str, dict[str, str]] = {}
    for m in STRUCT_RE.finditer(text):
        body = _function_body(text, m.group(0))
        fields: dict[str, str] = {}
        for raw in body.splitlines():
            line = raw.strip()
            if not line or line.startswith("//"):
                continue
            line = line.split("`", 1)[0].strip()
            parts = line.split()
            if len(parts) >= 2 and parts[0][0].isupper():
                fields[parts[0]] = parts[1]
        structs[m.group(1)] = fields
    return structs


def parse_consts(text: str) -> dict[str, str]:
    consts: dict[str, str] = {}
    for m in re.finditer(r"^const\s*(\(|\w)", text, re.MULTILINE):
        if m.group(1) == "(":
            body = text[m.end():text.find("\n)", m.end())]
            for raw in body.splitlines():
                cm = CONST_RE.match(raw)
                if cm:
                    consts[cm.group(1)] = cm.group(2)
        else:
            line = text[m.start():text.find("\n", m.start())]
            cm = re.match(r"const\s+(\w+)\s*=\s*(.+)$", line)
            if cm:
                consts[cm.group(1)] = cm.group(2).strip()
    return consts


def parse_default_literal(text: str) -> dict:
    """Parse `return &Config{ … }` in getDefaultConfig into nested dicts of raw expressions."""
    body = _function_body(text, "func getDefaultConfig()")
    start = body.find("&Config{")
    if start < 0:
        raise ConfigRefError("getDefaultConfig does not return &Config{...}")
    literal = _function_body(body, "&Config{")
    return _parse_composite(literal)


def _parse_composite(literal: str) -> dict:
    result: dict = {}
    stack: list[dict] = [result]
    for raw in literal.splitlines():
        line = raw.strip()
        if not line:
            continue
        if line == "},":
            stack.pop()
            continue
        m = re.match(r"^(\w+):\s*(.*)$", line)
        if not m:
            continue
        key, value = m.group(1), m.group(2).rstrip(",").strip()
        if re.match(r"^\w*Config\{$", value) or value.endswith("{") and "[]" not in value:
            child: dict = {}
            stack[-1][key] = child
            stack.append(child)
        else:
            stack[-1][key] = value
    return result


def _eval_expr(expr: str, consts: dict[str, str], depth: int = 0) -> int | float | None:
    """Evaluate the arithmetic Go uses in defaults: numbers, consts, `*`, `<<`, duration units."""
    if depth > 10:
        return None
    e = expr.strip()
    e = re.sub(r"\b(?:int64|int|float64)\((.*?)\)", r"\1", e)
    tokens = re.findall(r"time\.\w+|\d+(?:\.\d+)?|\w+|<<|\*|\+|-", e)
    if not tokens:
        return None
    acc: int | float | None = None
    op = "*"
    for t in tokens:
        if t in ("*", "<<", "+", "-"):
            op = t
            continue
        if t in DURATION_UNITS:
            v: int | float = DURATION_UNITS[t]
        elif re.fullmatch(r"\d+(?:\.\d+)?", t):
            v = float(t) if "." in t else int(t)
        elif t in consts:
            inner = _eval_expr(consts[t], consts, depth + 1)
            if inner is None:
                return None
            v = inner
        else:
            return None
        if acc is None:
            acc = v
        elif op == "*":
            acc = acc * v
        elif op == "<<":
            acc = int(acc) << int(v)
        elif op == "+":
            acc = acc + v
        elif op == "-":
            acc = acc - v
    return acc


def _format_bytes(n: int) -> str:
    for unit, size in (("GiB", 1 << 30), ("MiB", 1 << 20), ("KiB", 1 << 10)):
        if n >= size and n % size == 0:
            return f"{n} ({n // size} {unit})"
    return str(n)


def _format_duration(ns: int) -> str:
    for suffix, size in (("h", 3600 * 10**9), ("m", 60 * 10**9), ("s", 10**9)):
        if ns % size == 0:
            return f"{ns // size}{suffix}"
    return f"{ns}ns"


def render_default(raw: str | None, kind: str, go_type: str | None, consts: dict[str, str]) -> str:
    """Render a Go default expression the way an operator would write it in the environment."""
    if raw is None:
        # Go zero value of the field.
        if kind == "bool":
            return "`false`"
        if kind in ("int", "float", "bytes", "duration"):
            return "`0`"
        return "(empty)"
    raw = raw.strip()
    if raw.startswith('"') and raw.endswith('"'):
        return "(empty)" if raw == '""' else f"`{raw[1:-1]}`"
    if raw in ("true", "false"):
        return f"`{raw}`"
    if raw.startswith("[]string{"):
        items = re.findall(r'"([^"]*)"', raw)
        return ", ".join(f"`{i}`" for i in items) if items else "(empty)"
    if raw in consts and consts[raw].startswith('"'):
        inner = consts[raw]
        return "(empty)" if inner == '""' else f"`{inner[1:-1]}`"
    value = _eval_expr(raw, consts)
    if value is None:
        return f"`{raw}`"
    if kind == "duration" or go_type == "time.Duration":
        return f"`{_format_duration(int(value))}`"
    if kind == "bytes" or go_type == "int64":
        return f"`{_format_bytes(int(value))}`"
    if isinstance(value, float) and value.is_integer():
        value = int(value)
    return f"`{value}`"


# --------------------------------------------------------------------------- the loader


def load_source(repo: Path) -> GoSource:
    path = repo / CONFIG_GO
    if not path.is_file():
        raise ConfigRefError(f"config.go not found at {path}")
    text = _strip_comments(path.read_text(encoding="utf-8"))
    src = GoSource(text=text)
    src.structs = parse_structs(text)
    src.consts = parse_consts(text)
    src.defaults = parse_default_literal(text)
    return src


def _lookup(d: dict, path: list[str]):
    cur = d
    for p in path:
        if not isinstance(cur, dict) or p not in cur:
            return None
        cur = cur[p]
    return cur


def _field_type(src: GoSource, path: list[str]) -> str | None:
    struct = "Config"
    go_type = None
    for p in path:
        fields = src.structs.get(struct)
        if not fields or p not in fields:
            return None
        go_type = fields[p]
        struct = go_type
    return go_type


def parse_variables(src: GoSource) -> list[Variable]:
    body = _function_body(src.text, "func ApplyEnvironment(")
    offset = src.text.find(body)
    variables: dict[str, Variable] = {}
    for m in GETENV_RE.finditer(body):
        name = m.group(2)
        if name in variables:
            raise ConfigRefError(f"{name} is read twice in ApplyEnvironment")
        block = _statement_from(body, m.start())
        am = ASSIGN_RE.search(block)
        field_path = am.group(1) if am else None
        kind = _classify(block)
        line = src.text.count("\n", 0, offset + m.start()) + 1
        v = Variable(name=name, secret=m.group(1) == "secretEnv", kind=kind, field_path=field_path, line=line)
        path = field_path.split(".") if field_path else []
        raw = _lookup(src.defaults, path) if path else None
        if isinstance(raw, dict):
            raw = None
        go_type = _field_type(src, path) if path else None
        if field_path and go_type is None:
            raise ConfigRefError(f"{name}: the loader assigns cfg.{field_path}, which no struct declares")
        v.default = render_default(raw, kind, go_type, src.consts)
        variables[name] = v
    if not variables:
        raise ConfigRefError("no environment variables found in ApplyEnvironment")
    return list(variables.values())


def production_facts(src: GoSource) -> dict:
    """The constants the production-checks section quotes, read from the source."""
    markers_m = WEAK_MARKER_RE.search(src.text)
    envs_m = INSECURE_ENV_RE.search(src.text)
    min_m = MIN_SECRET_RE.search(src.text)
    if not (markers_m and envs_m and min_m):
        raise ConfigRefError("weakSecretMarkers, insecureEnvironments or minProductionSecretLength not found")
    # Problems() delegates the secret checks to secretProblems() — the subset
    # that refuses the boot in every environment — so the facts read both
    # bodies as one: a weak-secret call moved between them must stay visible.
    problems = _function_body(src.text, "func (c *Config) Problems()")
    if "func (c *Config) secretProblems()" in src.text:
        problems += _function_body(src.text, "func (c *Config) secretProblems()")
    return {
        "markers": re.findall(r'"([^"]+)"', markers_m.group(1)),
        "insecure_envs": re.findall(r'"([^"]+)"', envs_m.group(1)),
        "min_secret_length": int(min_m.group(1)),
        "weak_secret_names": WEAK_SECRET_CALL_RE.findall(problems),
        "problems_text": problems,
    }


# --------------------------------------------------------------------------- descriptions


def load_descriptions(path: Path) -> dict:
    try:
        return tomllib.loads(path.read_text(encoding="utf-8"))
    except (OSError, tomllib.TOMLDecodeError) as exc:
        raise ConfigRefError(f"cannot read {path}: {exc}") from exc


def reconcile(variables: list[Variable], desc: dict, facts: dict) -> tuple[list[Variable], dict[str, dict]]:
    """Every parsed variable needs a description; every description needs a variable.

    Entries marked `outside_loader = true` describe variables the server reads
    somewhere other than ApplyEnvironment; they carry their own type and default
    and must NOT also be read by the loader.
    """
    entries: dict[str, dict] = desc.get("variables", {})
    parsed = {v.name: v for v in variables}
    missing = sorted(n for n in parsed if n not in entries)
    if missing:
        raise ConfigRefError("no description for: " + ", ".join(missing)
                             + f" (add each to {DESCRIPTIONS.name})")
    all_vars = list(variables)
    stale = []
    for name, entry in entries.items():
        if entry.get("outside_loader"):
            if name in parsed:
                raise ConfigRefError(f"{name} is marked outside_loader but ApplyEnvironment reads it")
            for key in ("type", "default", "read_in"):
                if key not in entry:
                    raise ConfigRefError(f"{name}: outside_loader entries need `{key}`")
            all_vars.append(Variable(name=name, secret=bool(entry.get("secret")), kind=entry["type"],
                                     field_path=None, line=0, default=entry["default"]))
        elif name not in parsed:
            stale.append(name)
    if stale:
        raise ConfigRefError("described but not read by ApplyEnvironment: " + ", ".join(sorted(stale)))
    sections = desc.get("sections", {})
    order = sections.get("order", [])
    for v in all_vars:
        entry = entries[v.name]
        if not entry.get("description", "").strip():
            raise ConfigRefError(f"{v.name}: empty description")
        if "\n" in entry["description"].strip():
            raise ConfigRefError(f"{v.name}: the description must be one line")
        if entry.get("section") not in order:
            raise ConfigRefError(f"{v.name}: section {entry.get('section')!r} is not in sections.order")
    for sec in order:
        if sec not in sections or "title" not in sections[sec]:
            raise ConfigRefError(f"section {sec!r} has no [sections.{sec}] title")
    checks = desc.get("production_checks", [])
    for name in facts["weak_secret_names"]:
        if not any(name in c.get("variables", []) for c in checks):
            raise ConfigRefError(f"Problems() checks {name} for weakness but no production check names it")
    for c in checks:
        for name in c.get("variables", []):
            # Problems() names a variable directly in its message, or inspects
            # the field the loader assigns from it (`c.GRPC.TLSCertFile`).
            leaf = parsed[name].field_path.rsplit(".", 1)[-1] if name in parsed and parsed[name].field_path else None
            if name not in facts["problems_text"] and not (leaf and f".{leaf}" in facts["problems_text"]):
                raise ConfigRefError(f"production check names {name}, which Problems() does not mention")
        if not c.get("refuses") or not c.get("satisfy"):
            raise ConfigRefError("every production check needs `refuses` and `satisfy`")
    return all_vars, entries


# --------------------------------------------------------------------------- rendering

KIND_LABEL = {"string": "string", "int": "integer", "bytes": "bytes (integer)", "float": "number",
              "bool": "boolean", "duration": "duration", "list": "list (comma-separated)"}


def _cell(text: str) -> str:
    return text.replace("|", "\\|")


def render(variables: list[Variable], entries: dict[str, dict], desc: dict, facts: dict) -> str:
    sections = desc["sections"]
    out: list[str] = []
    out.append("# Configuration reference")
    out.append("")
    out.append("<!-- GENERATED by tools/configref/configref.py from the backend's configuration")
    out.append("     loader and tools/configref/descriptions.toml. Do not edit by hand: CI runs")
    out.append("     `configref.py --check` and fails when this page differs from the generator's")
    out.append("     output. Change the loader or the descriptions, then regenerate. -->")
    out.append("")
    out.append(desc.get("preamble", "").strip())
    out.append("")
    out.append("## How configuration is read")
    out.append("")
    out.append(desc.get("loading", "").strip())
    out.append("")
    out.append("## Secrets")
    out.append("")
    secret_names = sorted(v.name for v in variables if v.secret)
    out.append(desc.get("secrets_note", "").strip())
    out.append("")
    out.append("The variables read this way: " + ", ".join(f"`{n}`" for n in secret_names) + ".")
    out.append("")
    out.append("## Variables")
    out.append("")
    out.append("Types: *duration* is a Go duration (`15m`, `24h`, `30s`); *bytes* is a plain integer")
    out.append("count of bytes; *list* is comma-separated. A default of *(empty)* means the feature")
    out.append("or override is off until the variable is set. Where `0` has a special meaning the")
    out.append("description says so: it is \"unlimited\" for most quotas and \"the built-in default\"")
    out.append("for the admission bounds that have no unlimited setting.")
    out.append("")
    for sec in sections["order"]:
        meta = sections[sec]
        rows = [v for v in variables if entries[v.name]["section"] == sec]
        rows.sort(key=lambda v: (entries[v.name].get("order", 1000), v.name))
        out.append(f"### {meta['title']}")
        out.append("")
        if meta.get("intro", "").strip():
            out.append(meta["intro"].strip())
            out.append("")
        out.append("| Variable | Type | Default | What it does |")
        out.append("|---|---|---|---|")
        for v in rows:
            entry = entries[v.name]
            kind = KIND_LABEL.get(v.kind, v.kind)
            if v.secret:
                kind += ", secret"
            text = entry["description"].strip()
            if entry.get("outside_loader"):
                text += f" Read in `{entry['read_in']}`, not by the configuration loader."
            out.append(f"| `{v.name}` | {kind} | {_cell(v.default)} | {_cell(text)} |")
        out.append("")
    out.append("## Production checks")
    out.append("")
    out.append(desc.get("production_intro", "").strip()
               .replace("{insecure_envs}", ", ".join(f"`{e}`" for e in facts["insecure_envs"]))
               .replace("{min_secret_length}", str(facts["min_secret_length"]))
               .replace("{markers}", ", ".join(f"`{m}`" for m in facts["markers"])))
    out.append("")
    out.append("| The boot is refused when | How to satisfy it |")
    out.append("|---|---|")
    for c in desc.get("production_checks", []):
        out.append(f"| {_cell(c['refuses'].strip())} | {_cell(c['satisfy'].strip())} |")
    out.append("")
    if desc.get("advisories", "").strip():
        out.append("### Advisories")
        out.append("")
        out.append(desc["advisories"].strip())
        out.append("")
    return "\n".join(out).rstrip() + "\n"


def generate(repo: Path, descriptions: Path = DESCRIPTIONS) -> tuple[str, int]:
    src = load_source(repo)
    variables = parse_variables(src)
    facts = production_facts(src)
    desc = load_descriptions(descriptions)
    all_vars, entries = reconcile(variables, desc, facts)
    return render(all_vars, entries, desc, facts), len(variables)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--repo", default=str(Path(__file__).resolve().parents[2]), help="repository root")
    p.add_argument("--descriptions", default=str(DESCRIPTIONS), help="the descriptions sidecar")
    p.add_argument("--check", action="store_true", help="fail when the committed page is stale")
    p.add_argument("--stdout", action="store_true", help="print the page instead of writing it")
    args = p.parse_args(argv)
    repo = Path(args.repo)
    try:
        page, count = generate(repo, Path(args.descriptions))
    except ConfigRefError as exc:
        print(f"configref: {exc}", file=sys.stderr)
        return 1
    target = repo / OUTPUT_MD
    if args.stdout:
        sys.stdout.write(page)
        return 0
    if args.check:
        current = target.read_text(encoding="utf-8") if target.is_file() else ""
        if current != page:
            diff = difflib.unified_diff(current.splitlines(), page.splitlines(),
                                        fromfile=str(OUTPUT_MD), tofile="generated", lineterm="", n=1)
            sys.stdout.write("\n".join(list(diff)[:80]) + "\n")
            print(f"configref: {OUTPUT_MD} is stale; run tools/configref/configref.py", file=sys.stderr)
            return 1
        print(f"configref: {OUTPUT_MD} is current ({count} loader variables)", file=sys.stderr)
        return 0
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(page, encoding="utf-8")
    print(f"configref: wrote {OUTPUT_MD} ({count} loader variables)", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
