"""Tests for vulngate.py: what is reachable, what an acceptance may excuse and
what it may not.

Run: python3 tools/vulngate/vulngate_test.py
"""

from __future__ import annotations

import datetime as dt
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import vulngate  # noqa: E402

TODAY = dt.date(2026, 11, 18)


def stream(*findings: dict) -> str:
    messages = [{"config": {"scanner_name": "govulncheck"}}, *({"finding": f} for f in findings)]
    return "\n".join(json.dumps(m) for m in messages)


def called(osv: str) -> dict:
    return {"osv": osv, "trace": [{"module": "example.test/m", "package": "example.test/m/p", "function": "F"}]}


def imported(osv: str) -> dict:
    return {"osv": osv, "trace": [{"module": "example.test/m", "package": "example.test/m/p"}]}


class ReachableTest(unittest.TestCase):
    def test_only_a_function_frame_counts(self) -> None:
        found = vulngate.reachable(stream(called("GO-2026-0001"), imported("GO-2026-0002")))
        self.assertEqual(list(found), ["GO-2026-0001"])

    def test_a_stream_without_config_did_not_scan(self) -> None:
        with self.assertRaises(vulngate.GateError):
            vulngate.reachable(json.dumps({"finding": called("GO-2026-0001")}))

    def test_garbage_is_not_a_clean_scan(self) -> None:
        with self.assertRaises(vulngate.GateError):
            vulngate.reachable("go: downloading something\n")


class JudgeTest(unittest.TestCase):
    def accept(self, text: str) -> dict:
        return vulngate.parse_accepted(text)

    def test_an_unaccepted_reachable_advisory_fails(self) -> None:
        problems = vulngate.judge({"GO-2026-0001": "p.F"}, {}, TODAY)
        self.assertEqual(len(problems), 1)
        self.assertIn("not accepted", problems[0])

    def test_a_live_acceptance_passes(self) -> None:
        accepted = self.accept("GO-2026-0001 2027-01-01 read, does not apply\n")
        self.assertEqual(vulngate.judge({"GO-2026-0001": "p.F"}, accepted, TODAY), [])

    def test_an_acceptance_is_good_through_its_own_date(self) -> None:
        accepted = self.accept("GO-2026-0001 2026-11-18 due today\n")
        self.assertEqual(vulngate.judge({"GO-2026-0001": "p.F"}, accepted, TODAY), [])

    def test_an_expired_acceptance_fails(self) -> None:
        accepted = self.accept("GO-2026-0001 2026-11-17 was due yesterday\n")
        problems = vulngate.judge({"GO-2026-0001": "p.F"}, accepted, TODAY)
        self.assertEqual(len(problems), 1)
        self.assertIn("due for review", problems[0])

    def test_a_stale_acceptance_fails(self) -> None:
        accepted = self.accept("GO-2026-0001 2027-01-01 no longer reported\n")
        problems = vulngate.judge({}, accepted, TODAY)
        self.assertEqual(len(problems), 1)
        self.assertIn("delete the entry", problems[0])

    def test_one_acceptance_does_not_excuse_another_advisory(self) -> None:
        accepted = self.accept("GO-2026-0001 2027-01-01 this one only\n")
        problems = vulngate.judge({"GO-2026-0001": "p.F", "GO-2026-0002": "p.G"}, accepted, TODAY)
        self.assertEqual(len(problems), 1)
        self.assertIn("GO-2026-0002", problems[0])


class AcceptedFileTest(unittest.TestCase):
    def test_comments_and_blank_lines(self) -> None:
        accepted = vulngate.parse_accepted("# why\n\nGO-2026-0001 2027-01-01 a reason  # trailing\n")
        self.assertEqual(accepted, {"GO-2026-0001": (dt.date(2027, 1, 1), "a reason")})

    def test_an_entry_needs_a_date_and_a_reason(self) -> None:
        for bad in ("GO-2026-0001\n", "GO-2026-0001 2027-01-01\n", "GO-2026-0001 soon a reason\n",
                    "not-an-id 2027-01-01 a reason\n", "GO-2026-0001 2027-01-01 a\nGO-2026-0001 2027-01-01 b\n"):
            with self.subTest(bad=bad), self.assertRaises(vulngate.GateError):
                vulngate.parse_accepted(bad)


class MainTest(unittest.TestCase):
    def run_main(self, findings: str, accepted: str | None) -> int:
        with tempfile.TemporaryDirectory() as tmp:
            scan = Path(tmp) / "scan.json"
            scan.write_text(findings)
            argv = ["--json", str(scan), "--today", TODAY.isoformat()]
            if accepted is not None:
                path = Path(tmp) / "accepted.txt"
                path.write_text(accepted)
                argv += ["--accepted", str(path)]
            return vulngate.main(argv)

    def test_exit_codes(self) -> None:
        self.assertEqual(self.run_main(stream(), None), 0)
        self.assertEqual(self.run_main(stream(imported("GO-2026-0002")), None), 0)
        self.assertEqual(self.run_main(stream(called("GO-2026-0001")), None), 1)
        self.assertEqual(self.run_main(stream(called("GO-2026-0001")), "GO-2026-0001 2027-01-01 read\n"), 0)
        self.assertEqual(self.run_main("not json", None), 2)
        self.assertEqual(self.run_main(stream(), "GO-2026-0001 never a reason\n"), 2)

    def test_a_missing_acceptance_file_is_not_an_empty_one(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            scan = Path(tmp) / "scan.json"
            scan.write_text(stream())
            self.assertEqual(vulngate.main(["--json", str(scan), "--accepted", str(Path(tmp) / "absent.txt")]), 2)


if __name__ == "__main__":
    unittest.main()
