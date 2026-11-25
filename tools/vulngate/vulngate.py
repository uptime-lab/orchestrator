#!/usr/bin/env python3
"""Gate a Go module on govulncheck, with written, dated acceptances.

govulncheck has no way to say "this advisory was read and does not apply
here", so a single advisory that has no fixed release keeps its job red for
good, and a job that is always red is read by nobody. This wrapper runs the
scanner, keeps its judgement of what is REACHABLE, and subtracts only the
advisories listed in an acceptance file, each with a review date and a reason.

What still fails the run (exit 1):
  * a reachable advisory that is not accepted;
  * an acceptance whose review date has passed -- an acceptance is a loan;
  * an acceptance the scanner no longer reports -- a stale entry is a hole
    nobody sees, so it has to be deleted when its advisory goes away.

Usage:
  vulngate.py [--accepted FILE] [--today YYYY-MM-DD] [--json FILE] [-- ARGS...]

Run it from the module directory, with that module's own acceptance file: an
entry is judged against one module's scan, so a shared file would be stale
for every module that does not report the advisory. ARGS go to govulncheck
before the package pattern (for example `-- -tags piv`). --json reads a saved `govulncheck
-format json` stream instead of running the scanner, which is what the tests
use.

Acceptance file: one entry per line, `#` starts a comment.
  <OSV id>  <review-by YYYY-MM-DD>  <reason, free text>

Exit 0 clean, 1 as above, 2 when the scanner could not run or its output or
the acceptance file could not be read: a gate that could not look has not
passed.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import subprocess
import sys
from pathlib import Path

SCANNER = ["go", "run", "golang.org/x/vuln/cmd/govulncheck@latest", "-format", "json"]
OSV_ID = re.compile(r"^[A-Z]+-\d{4}-\d+$")


class GateError(Exception):
    """The gate could not look: unreadable input, a scanner that did not run."""


def parse_accepted(text: str) -> dict[str, tuple[dt.date, str]]:
    accepted: dict[str, tuple[dt.date, str]] = {}
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        parts = line.split(None, 2)
        if len(parts) < 3:
            raise GateError(f"acceptance line {number}: expected `<id> <review-by> <reason>`")
        osv, day, reason = parts
        if not OSV_ID.match(osv):
            raise GateError(f"acceptance line {number}: {osv!r} is not an advisory id")
        try:
            review_by = dt.date.fromisoformat(day)
        except ValueError as exc:
            raise GateError(f"acceptance line {number}: {day!r} is not a date (YYYY-MM-DD)") from exc
        if osv in accepted:
            raise GateError(f"acceptance line {number}: {osv} is listed twice")
        accepted[osv] = (review_by, reason)
    return accepted


def reachable(stream: str) -> dict[str, str]:
    """Advisory id -> one example frame, for findings the scanner traced to a
    function. A finding without a function frame is "the module is required"
    or "the package is imported", which govulncheck itself does not fail on.
    """
    decoder = json.JSONDecoder()
    found: dict[str, str] = {}
    index, seen_config = 0, False
    while True:
        while index < len(stream) and stream[index].isspace():
            index += 1
        if index >= len(stream):
            break
        try:
            message, index = decoder.raw_decode(stream, index)
        except json.JSONDecodeError as exc:
            raise GateError(f"scanner output is not a JSON stream: {exc}") from exc
        if "config" in message:
            seen_config = True
        finding = message.get("finding")
        if not finding:
            continue
        frames = [f for f in finding.get("trace", []) if f.get("function")]
        if frames:
            frame = frames[0]
            found.setdefault(finding["osv"], f"{frame.get('package', frame.get('module', '?'))}.{frame['function']}")
    if not seen_config:
        raise GateError("scanner output carries no config message: it did not run to the scan")
    return found


def judge(found: dict[str, str], accepted: dict[str, tuple[dt.date, str]], today: dt.date) -> list[str]:
    problems = []
    for osv in sorted(found):
        if osv not in accepted:
            problems.append(f"{osv}: reachable through {found[osv]} and not accepted (https://pkg.go.dev/vuln/{osv})")
        elif accepted[osv][0] < today:
            problems.append(f"{osv}: its acceptance was due for review on {accepted[osv][0]}; re-argue it or fix it")
    for osv in sorted(set(accepted) - set(found)):
        problems.append(f"{osv}: accepted but no longer reported here; delete the entry")
    return problems


def scan(extra: list[str]) -> str:
    try:
        done = subprocess.run([*SCANNER, *extra, "./..."], capture_output=True, text=True, check=False)
    except OSError as exc:
        raise GateError(f"cannot run the scanner: {exc}") from exc
    # In JSON mode the scanner exits 0 with findings; anything else is a failure to scan.
    if done.returncode != 0:
        raise GateError(f"the scanner exited {done.returncode}: {done.stderr.strip()[-2000:]}")
    return done.stdout


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--accepted", type=Path, help="acceptance file; without it nothing is accepted")
    parser.add_argument("--today", type=dt.date.fromisoformat, default=None, help="date to judge review dates against")
    parser.add_argument("--json", type=Path, help="read a saved scanner JSON stream instead of scanning")
    parser.add_argument("extra", nargs="*", help="arguments for govulncheck, after `--`")
    args = parser.parse_args(argv)
    today = args.today or dt.date.today()
    try:
        accepted = parse_accepted(args.accepted.read_text(encoding="utf-8")) if args.accepted else {}
        stream = args.json.read_text(encoding="utf-8") if args.json else scan(args.extra)
        found = reachable(stream)
    except (GateError, OSError) as exc:
        print(f"vulngate: cannot judge: {exc}", file=sys.stderr)
        return 2
    problems = judge(found, accepted, today)
    for osv in sorted(set(found) & set(accepted)):
        if accepted[osv][0] >= today:
            print(f"accepted until {accepted[osv][0]}: {osv} -- {accepted[osv][1]}")
    for problem in problems:
        print(f"FAIL {problem}", file=sys.stderr)
    if problems:
        return 1
    print(f"vulngate: {len(found)} reachable advisory(ies), all accepted" if found else "vulngate: no reachable advisory")
    return 0


if __name__ == "__main__":
    sys.exit(main())
