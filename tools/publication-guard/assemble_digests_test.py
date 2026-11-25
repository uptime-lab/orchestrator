#!/usr/bin/env python3
"""The digest transport, end to end, through the SAME code the workflow runs.

`guard.yml`'s `private-identifier-guard` job receives the digest payload as
the environment secrets PUBLICATION_GUARD_DIGESTS_1..12, joins them with
`assemble_digests.sh`, verifies the result with `private_identifiers_ci.py
verify-digests` and scans with `check --digests`. These tests generate parts
from a SYNTHETIC list, hand them to the real script as the same environment
variables, and drive the real checker: a listed identifier in a tree is
found (exit 1), a clean tree passes (exit 0), and every way a part can go
missing or wrong is a hard failure (exit 2), never a smaller table.

They also pin the three places that must agree on the number of parts: the
checker's MAX_DIGEST_PARTS, the script's MAX_PARTS and the env names the
workflow declares.
"""

from __future__ import annotations

import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
CHECKER = HERE / "private_identifiers_ci.py"
ASSEMBLER = HERE / "assemble_digests.sh"
GUARD_YML = ROOT / ".github" / "workflows" / "guard.yml"

sys.path.insert(0, str(HERE))

import private_identifiers_ci as pi  # noqa: E402

# Synthetic only. Enough enumerated networks that the payload needs several
# parts at the real bound, which is what the transport exists for.
SYNTHETIC_LIST = "".join(
    [
        "serial16\t10000000abcd1234\tfixture\n",
        "hostname\tbuildbox7.example.internal\n",
        "misc\topsuser\n",
        "cidr\t198.18.0.0/20\n",
        "cidr\t198.19.16.0/21\n",
    ]
)
LISTED = "the lab address 198.19.20.7 must not be published\n"
CLEAN = "nothing private here, only 192.0.2.1 from the documentation range\n"
KEY = "fixture-key-not-a-secret"


def run(cmd: list[str], env: dict[str, str] | None = None, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(cmd, env=env, cwd=cwd, capture_output=True, text=True, check=False)


class Transport(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.list_path = self.dir / "list.txt"
        self.list_path.write_text(SYNTHETIC_LIST, encoding="utf-8")
        self.parts_dir = self.dir / "parts"
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(pi.DIGEST_PART_PREFIX)}
        self.env["PI_TEST_KEY"] = KEY
        res = run([sys.executable, str(CHECKER), "digest", "--list", str(self.list_path),
                   "--key-env", "PI_TEST_KEY", "--out-dir", str(self.parts_dir)], env=self.env)
        self.assertEqual(res.returncode, 0, res.stderr)
        self.parts = sorted(self.parts_dir.iterdir(), key=lambda p: int(p.name.rsplit("_", 1)[1]))
        self.assertGreater(len(self.parts), 1, "the fixture must need several parts")
        self.dirty = self.dir / "dirty"
        self.dirty.mkdir()
        (self.dirty / "notes.md").write_text(LISTED, encoding="utf-8")
        self.clean = self.dir / "clean"
        self.clean.mkdir()
        (self.clean / "notes.md").write_text(CLEAN, encoding="utf-8")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def secrets(self) -> dict[str, str]:
        """The parts as the workflow's env: one variable per secret, same content."""
        return {p.name: p.read_text(encoding="utf-8") for p in self.parts}

    def assemble(self, secrets: dict[str, str]) -> tuple[subprocess.CompletedProcess[str], Path]:
        out = self.dir / "guard.digests"
        if out.exists():
            out.unlink()
        res = run(["bash", str(ASSEMBLER), str(out)], env={**self.env, **secrets})
        return res, out

    def verify(self, payload: Path) -> subprocess.CompletedProcess[str]:
        return run([sys.executable, str(CHECKER), "verify-digests", "--digests", str(payload)], env=self.env)

    def scan(self, payload: Path, tree: Path) -> subprocess.CompletedProcess[str]:
        return run([sys.executable, str(CHECKER), "check", "--digests", str(payload),
                    "--key-env", "PI_TEST_KEY", "--tree", str(tree)], env=self.env)

    # -- the parts themselves

    def test_every_part_is_under_the_secret_bound(self) -> None:
        for part in self.parts:
            self.assertLessEqual(part.stat().st_size, pi.MAX_DIGEST_PART_BYTES, part.name)
        self.assertLessEqual(len(self.parts), pi.MAX_DIGEST_PARTS)
        self.assertEqual([p.name for p in self.parts],
                         [f"{pi.DIGEST_PART_PREFIX}{i}" for i in range(1, len(self.parts) + 1)])

    # -- reassembled exactly as the workflow does

    def test_reassembled_payload_finds_a_listed_identifier_and_passes_a_clean_tree(self) -> None:
        res, payload = self.assemble(self.secrets())
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(self.verify(payload).returncode, 0)
        hit = self.scan(payload, self.dirty)
        self.assertEqual(hit.returncode, 1, hit.stdout + hit.stderr)
        self.assertRegex(hit.stdout, r"notes\.md:1:cidr: hmac:[0-9a-f]{6}")
        self.assertNotIn("198.19.20.7", hit.stdout + hit.stderr)
        ok = self.scan(payload, self.clean)
        self.assertEqual(ok.returncode, 0, ok.stdout + ok.stderr)

    def test_a_missing_middle_part_fails_hard(self) -> None:
        secrets = self.secrets()
        secrets.pop(f"{pi.DIGEST_PART_PREFIX}2")
        res, _ = self.assemble(secrets)
        self.assertGreaterEqual(res.returncode, 2)
        self.assertIn("PUBLICATION_GUARD_DIGESTS_2 is empty or unset", res.stderr)

    def test_an_empty_part_fails_hard(self) -> None:
        # An unset secret reaches the step as an empty string.
        secrets = self.secrets()
        secrets[f"{pi.DIGEST_PART_PREFIX}{len(self.parts)}"] = ""
        res, _ = self.assemble(secrets)
        self.assertGreaterEqual(res.returncode, 2)

    def test_no_parts_at_all_fails_hard(self) -> None:
        res, _ = self.assemble({})
        self.assertGreaterEqual(res.returncode, 2)

    def test_a_stale_part_beyond_n_fails_hard(self) -> None:
        secrets = self.secrets()
        self.assertLess(len(self.parts), pi.MAX_DIGEST_PARTS)
        secrets[f"{pi.DIGEST_PART_PREFIX}{len(self.parts) + 1}"] = secrets[f"{pi.DIGEST_PART_PREFIX}1"]
        res, _ = self.assemble(secrets)
        self.assertGreaterEqual(res.returncode, 2)
        self.assertIn("stale", res.stderr)

    def test_a_truncated_part_fails_verification_and_the_scan(self) -> None:
        secrets = self.secrets()
        name = f"{pi.DIGEST_PART_PREFIX}2"
        lines = secrets[name].splitlines()
        secrets[name] = "\n".join(lines[: len(lines) // 2]) + "\n"
        res, payload = self.assemble(secrets)
        self.assertEqual(res.returncode, 0, "the shell only sees a non-empty part; the checker must refuse it")
        self.assertEqual(self.verify(payload).returncode, 2)
        # The scan refuses too, so even a workflow that skipped the verify
        # step would fail rather than check against a smaller table.
        self.assertEqual(self.scan(payload, self.clean).returncode, 2)

    def test_a_corrupt_row_fails_verification(self) -> None:
        secrets = self.secrets()
        name = f"{pi.DIGEST_PART_PREFIX}1"
        lines = secrets[name].splitlines()
        cat, tag = lines[1].split("\t")
        lines[1] = f"{cat}\t{'0' if tag[0] != '0' else '1'}{tag[1:]}"
        secrets[name] = "\n".join(lines) + "\n"
        _, payload = self.assemble(secrets)
        self.assertEqual(self.verify(payload).returncode, 2)

    def test_swapped_parts_fail_hard(self) -> None:
        secrets = self.secrets()
        a, b = f"{pi.DIGEST_PART_PREFIX}1", f"{pi.DIGEST_PART_PREFIX}2"
        secrets[a], secrets[b] = secrets[b], secrets[a]
        res, payload = self.assemble(secrets)
        if res.returncode == 0:
            self.assertEqual(self.verify(payload).returncode, 2)
        else:
            self.assertGreaterEqual(res.returncode, 2)

    def test_parts_of_two_different_uploads_fail_verification(self) -> None:
        # Rotation half done: part 1 from the new upload, the rest from the old.
        other = self.dir / "other"
        env = {**self.env, "PI_TEST_KEY": KEY + "-rotated"}
        res = run([sys.executable, str(CHECKER), "digest", "--list", str(self.list_path),
                   "--key-env", "PI_TEST_KEY", "--out-dir", str(other)], env=env)
        self.assertEqual(res.returncode, 0, res.stderr)
        secrets = self.secrets()
        secrets[f"{pi.DIGEST_PART_PREFIX}1"] = (other / f"{pi.DIGEST_PART_PREFIX}1").read_text(encoding="utf-8")
        _, payload = self.assemble(secrets)
        self.assertEqual(self.verify(payload).returncode, 2)

    def test_a_whole_payload_with_another_key_is_refused(self) -> None:
        # Rotation half done the other way: every part new, the key still old.
        # The payload verifies, but scanning with the wrong key would match
        # nothing -- a green job over a blind check -- so the scan refuses.
        res, payload = self.assemble(self.secrets())
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(self.verify(payload).returncode, 0)
        env = {**self.env, "PI_TEST_KEY": KEY + "-old"}
        res = run([sys.executable, str(CHECKER), "check", "--digests", str(payload),
                   "--key-env", "PI_TEST_KEY", "--tree", str(self.dirty)], env=env)
        self.assertEqual(res.returncode, 2, res.stdout + res.stderr)
        self.assertIn("not the one these digests were made with", res.stderr)

    def test_a_headerless_v1_file_is_refused(self) -> None:
        legacy = self.dir / "legacy.digests"
        legacy.write_text("misc\t" + "ab" * 16 + "\n", encoding="utf-8")
        self.assertEqual(self.verify(legacy).returncode, 2)
        self.assertEqual(self.scan(legacy, self.clean).returncode, 2)


class OneNumber(unittest.TestCase):
    """MAX_DIGEST_PARTS, the script's MAX_PARTS and the workflow's env agree."""

    def test_the_script_reads_as_many_parts_as_the_checker_writes(self) -> None:
        m = re.search(r"^MAX_PARTS=(\d+)$", ASSEMBLER.read_text(encoding="utf-8"), re.M)
        self.assertIsNotNone(m)
        self.assertEqual(int(m.group(1)), pi.MAX_DIGEST_PARTS)

    def test_the_workflow_declares_exactly_the_part_secrets(self) -> None:
        if not GUARD_YML.is_file():
            self.skipTest("guard.yml is not in this tree")
        text = GUARD_YML.read_text(encoding="utf-8")
        declared = re.findall(r"^\s+(PUBLICATION_GUARD_DIGESTS_\d+): \$\{\{ secrets\.(PUBLICATION_GUARD_DIGESTS_\d+) \}\}$",
                              text, re.M)
        self.assertEqual([env for env, _ in declared], [secret for _, secret in declared])
        self.assertEqual([env for env, _ in declared],
                         [f"{pi.DIGEST_PART_PREFIX}{i}" for i in range(1, pi.MAX_DIGEST_PARTS + 1)])
        self.assertNotRegex(text, r"secrets\.PUBLICATION_GUARD_DIGESTS\b(?!_)",
                            "the single-secret transport is gone; nothing may read it")

    def test_the_workflow_runs_mains_assembler_and_verifies(self) -> None:
        if not GUARD_YML.is_file():
            self.skipTest("guard.yml is not in this tree")
        text = GUARD_YML.read_text(encoding="utf-8")
        self.assertIn('git show "origin/main:tools/publication-guard/$f"', text)
        self.assertIn("for f in private_identifiers_ci.py assemble_digests.sh; do", text)
        self.assertIn('bash "$RUNNER_TEMP/assemble_digests.sh" "$RUNNER_TEMP/guard.digests"', text)
        self.assertIn('private_identifiers_ci.py" verify-digests --digests "$RUNNER_TEMP/guard.digests"', text)


if __name__ == "__main__":
    unittest.main()
