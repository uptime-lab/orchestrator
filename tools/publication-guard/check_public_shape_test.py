#!/usr/bin/env python3
"""Tests for check_public_shape.py.

Every fixture that must TRIP a rule is assembled by concatenation at runtime,
so this file holds none of the shapes it tests for and passes the checker when
the checker scans its own directory.
"""

from __future__ import annotations

import io
import os
import random
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import check_public_shape as cps  # noqa: E402


def checker() -> cps.ShapeChecker:
    return cps.ShapeChecker(cps.SyntheticAllow(cps.DEFAULT_ALLOW_SYNTHETIC), cps.DomainAllow(cps.DEFAULT_ALLOW_DOMAINS))


def rules_for(line: str, is_code: bool = False) -> set[str]:
    return {rule for rule, _ in checker().check_line(line, is_code)}


# Shapes assembled at runtime (see the module docstring).
REAL_SERIAL = "10000000" + "9f3e" + "2d1c"          # a serial prefix with a random tail
OLD_SERIAL = "00000000" + "7a6b" + "5c4d"
RANDOM_HEX16 = "4e" + "7c9a1b2d" + "3f5e6a"  # no prefix, still 16 hex
REAL_MAC = "b8:27:eb" + ":" + "4a:5b:6c"
REAL_MAC_DASH = "dc-a6-32" + "-" + "11:22:33".replace(":", "-")
PRIVATE_IP = "10" + ".9" + ".8.7"
CGNAT_IP = "100" + ".65" + ".1.2"
PUBLIC_IP = "11" + ".22" + ".33.44"
MAC_HOME = "/Users" + "/someone/Documents"
LINUX_HOME = "/home" + "/someone/project"
PERSON_EMAIL = "alice@" + "some-employer" + ".net"
INTERNAL_HOST = "build01.corp." + "some-employer" + ".net"
FINDING_ID = "SA" + "-99"
SEC_ID = "SEC " + "L-99"
AUD_ID = "AUD-" + "M-99"
QA_ID = "QA-" + "2000-01-01"
BACKLOG_REF = "backlog " + "999"
ROUND_REF = "the audit " + "round " + "9"
DATED_MEASUREMENT = "measured " + "2000-01-" + "01 on cloud"


class SerialShape(unittest.TestCase):
    def test_serial_prefix_trips(self) -> None:
        self.assertIn("serial-shape", rules_for(f"serial {REAL_SERIAL}"))
        self.assertIn("serial-shape", rules_for(f"serial {OLD_SERIAL}"))

    def test_any_hex16_outside_allow_list_trips(self) -> None:
        self.assertIn("serial-shape", rules_for(f"token {RANDOM_HEX16}"))

    def test_node_name_tail_trips(self) -> None:
        self.assertIn("serial-shape", rules_for("Node-" + REAL_SERIAL[8:]))
        self.assertIn("serial-shape", rules_for("rpi-" + REAL_SERIAL[8:]))

    def test_synthetic_serials_pass(self) -> None:
        for s in ("10000000deadbeef", "10000000abcd0001", "1000000000000001", "0000000000001234",
                  "ffffffffffffffff", "1000000011111111", "0123456789abcdef", "Node-deadbeef", "rpi-00000000"):
            self.assertEqual(set(), rules_for(f"example {s}"), s)

    def test_longer_hex_is_not_a_serial(self) -> None:
        sha = "9f3e2d1c" * 8  # a 64-hex digest
        self.assertEqual(set(), rules_for(f"sha256 {sha}"))

    def test_random_serial_is_not_allowed_by_the_list(self) -> None:
        """The allow list must admit only obviously synthetic shapes, never a random tail."""
        allow = cps.SyntheticAllow(cps.DEFAULT_ALLOW_SYNTHETIC)
        rng = random.Random(20260101)
        for _ in range(200):
            tail = f"{rng.getrandbits(32):08x}"
            full = f"{rng.getrandbits(64):016x}"
            self.assertFalse(allow.serial_allowed("10000000" + tail), tail)
            self.assertFalse(allow.serial_allowed("00000000" + tail), tail)
            self.assertFalse(allow.serial_allowed(full), full)


class PrivateIP(unittest.TestCase):
    def test_private_and_cgnat_trip(self) -> None:
        self.assertIn("private-ip", rules_for(f"host {PRIVATE_IP}"))
        self.assertIn("private-ip", rules_for(f"host {CGNAT_IP}"))
        self.assertIn("private-ip", rules_for("net 192.168" + ".7.0/24"))

    def test_public_outside_allow_set_trips(self) -> None:
        self.assertIn("private-ip", rules_for(f"root@{PUBLIC_IP}"))

    def test_documentation_and_example_addresses_pass(self) -> None:
        for ip in ("192.0.2.10", "198.51.100.7", "203.0.113.200", "127.0.0.1", "0.0.0.0", "255.255.255.0",
                   "8.8.8.8", "1.1.1.1", "169.254.169.254", "10.0.0.1", "192.168.1.100", "172.16.0.5",
                   "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "224.0.0.1"):
            self.assertEqual(set(), rules_for(f"addr {ip}"), ip)

    def test_version_numbers_pass(self) -> None:
        self.assertEqual(set(), rules_for("protoc v1.36.11.2 and 0.7.9.1"))


class MacRealOUI(unittest.TestCase):
    def test_vendor_mac_trips(self) -> None:
        self.assertIn("mac-real-oui", rules_for(f"mac {REAL_MAC}"))
        self.assertIn("mac-real-oui", rules_for(f"mac {REAL_MAC_DASH}"))

    def test_locally_administered_passes(self) -> None:
        self.assertEqual(set(), rules_for("mac 02:42:ac:11:00:02"))
        self.assertEqual(set(), rules_for("mac 0a:1b:2c:3d:4e:5f"))

    def test_vendor_prefix_with_synthetic_nic_passes(self) -> None:
        self.assertEqual(set(), rules_for("mac b8:27:eb:00:00:00"))
        self.assertEqual(set(), rules_for("mac dc:a6:32:12:34:56"))
        self.assertEqual(set(), rules_for("mac e4:5f:01:aa:aa:aa"))


class HomePath(unittest.TestCase):
    def test_home_paths_trip(self) -> None:
        self.assertIn("home-path", rules_for(f"cd {MAC_HOME}"))
        self.assertIn("home-path", rules_for(f"cd {LINUX_HOME}"))

    def test_placeholders_pass(self) -> None:
        for p in ("/home/user/x", "/home/example/.ssh", "/home/username", "/home/<user>/bin", "/home/$USER/x",
                  "pages/home/index.tsx", "src/home/", "/users/me"):
            self.assertEqual(set(), rules_for(f"path {p}"), p)


class EmailDomain(unittest.TestCase):
    def test_person_email_trips(self) -> None:
        self.assertIn("email-domain", rules_for(f"contact {PERSON_EMAIL}"))

    def test_example_domains_pass(self) -> None:
        for e in ("a@example.com", "b@example.org", "root@localhost", "x@test", "me@orchestrator.sh",
                  "123+bot@users.noreply.github.com", "git@github.com:org/repo"):
            self.assertEqual(set(), rules_for(f"mail {e}"), e)

    def test_example_star_matches_only_the_second_level_label(self) -> None:
        # `example.*` is example.<tld> and hosts under it, never a host that
        # merely has an `example` label somewhere in the middle.
        self.assertEqual(set(), rules_for("mail a@sub.example.net"))
        self.assertIn("email-domain", rules_for("mail me@example." + "some-employer" + ".net"))
        self.assertIn("internal-hostname", rules_for("host build.example." + "some-employer" + ".net"))

    def test_module_version_is_not_an_email(self) -> None:
        self.assertEqual(set(), rules_for("go install example.com/tool/protobuf@v1.36.12"))

    def test_pins_and_tags_are_not_emails(self) -> None:
        for s in ("uses: actions/checkout@v4", "FROM alpine:3@sha256:0123", "tool@latest", "x@" + "9" * 40):
            self.assertEqual(set(), rules_for(s), s)


class InternalHostname(unittest.TestCase):
    def test_internal_host_trips(self) -> None:
        self.assertIn("internal-hostname", rules_for(f"ssh {INTERNAL_HOST}"))
        self.assertIn("internal-hostname", rules_for(f"ssh {INTERNAL_HOST}", is_code=True))

    def test_public_and_reserved_names_pass(self) -> None:
        for h in ("app.orchestrator.sh", "api.github.com", "raw.githubusercontent.com", "www.example.com",
                  "host.docker.internal", "printer.local", "nas.lan", "golang.org/x/net", "go.uber.org/zap",
                  "pkg.go.dev", "example.net"):
            self.assertEqual(set(), rules_for(f"name {h}"), h)

    def test_identifiers_in_code_pass(self) -> None:
        for s in ("logger.info(msg)", "this.logger.info", "node.ou.id", "import.meta.env.dev", "u.host", "user.email",
                  "node.name", "cfg.app", "fs.is", "README.md", "install.sh", "check.py", "x.y.z.info"):
            self.assertEqual(set(), rules_for(f"{s}", is_code=True), s)


class IdToken(unittest.TestCase):
    def test_tracking_ids_trip(self) -> None:
        for s in (FINDING_ID, SEC_ID, AUD_ID, QA_ID, BACKLOG_REF, ROUND_REF, DATED_MEASUREMENT):
            self.assertIn("id-token", rules_for(f"see {s}"), s)

    def test_neutral_text_passes(self) -> None:
        for s in ("RSA-2048 keys", "round 2 of the tournament", "released on 2000-01-01", "the backlog grows",
                  "version 1.2.3 measured", "SA-NN placeholder", "AUD-X-NN placeholder", "ECDSA-P256"):
            self.assertEqual(set(), rules_for(s), s)


class OptOut(unittest.TestCase):
    def test_marker_silences_named_rule_and_is_counted(self) -> None:
        scanner = cps.Scanner(checker())
        scanner.scan_text("f.md", f"host {PRIVATE_IP}  # publication-guard: allow private-ip\n", is_code=False)
        self.assertEqual([], scanner.findings)
        self.assertEqual([("f.md", 1, "private-ip")], scanner.opt_outs)

    def test_marker_does_not_silence_another_rule(self) -> None:
        scanner = cps.Scanner(checker())
        scanner.scan_text("f.md", f"{PRIVATE_IP} {REAL_SERIAL} <!-- publication-guard: allow private-ip -->\n", is_code=False)
        self.assertEqual({"serial-shape"}, {f.rule for f in scanner.findings})


class FileRules(unittest.TestCase):
    def test_large_file_trips(self) -> None:
        scanner = cps.Scanner(checker())
        scanner.scan_file_bytes("big.bin", b"a" * (cps.LARGE_FILE_BYTES + 1), executable_bit=False)
        self.assertEqual({"large-file"}, {f.rule for f in scanner.findings})

    def test_executable_magic_trips(self) -> None:
        for head, kind in ((b"\x7fELF" + b"\x00" * 60, "ELF"), (b"\xcf\xfa\xed\xfe" + b"\x00" * 60, "Mach-O"),
                           (b"MZ" + b"\x00" * 60, "PE"), (b"\xca\xfe\xba\xbe" + b"\x00" * 60, "universal")):
            scanner = cps.Scanner(checker())
            scanner.scan_file_bytes("blob", head, executable_bit=False)
            self.assertEqual({"executable-magic"}, {f.rule for f in scanner.findings}, kind)

    def test_executable_bit_on_binary_without_shebang_trips(self) -> None:
        scanner = cps.Scanner(checker())
        scanner.scan_file_bytes("tool", b"\x01\x02\x00\x03", executable_bit=True)
        self.assertEqual({"executable-magic"}, {f.rule for f in scanner.findings})

    def test_executable_script_with_shebang_passes(self) -> None:
        scanner = cps.Scanner(checker())
        scanner.scan_file_bytes("tool.sh", b"#!/usr/bin/env bash\necho ok\n", executable_bit=True)
        self.assertEqual([], scanner.findings)

    def test_skipped_paths_keep_file_rules_but_not_text_rules(self) -> None:
        scanner = cps.Scanner(checker())
        scanner.scan_file_bytes("backend/go.sum", f"{PRIVATE_IP}\n".encode(), executable_bit=False)
        scanner.scan_file_bytes("x/gen.pb.go", f"{REAL_SERIAL}\n".encode(), executable_bit=False)
        scanner.scan_file_bytes("frontend/dist/app.js", b"\x7fELF" + b"\x00" * 8, executable_bit=False)
        self.assertEqual({"executable-magic"}, {f.rule for f in scanner.findings})

    def test_generated_names_are_skipped_only_at_their_own_paths(self) -> None:
        for path in ("THIRD-PARTY-NOTICES.md", "frontend/public/THIRD-PARTY-NOTICES.md",
                     "frontend/pnpm-lock.yaml", "tools/agent-signer/go.sum", "frontend/dist/assets/a.js"):
            with self.subTest(path=path):
                self.assertTrue(cps.is_text_skipped(path))
        # The stray notices path is concatenated: the doc-citation check reads
        # a docs/<NAME>.md literal as a citation of a document that must exist.
        for path in ("docs" + "/THIRD-PARTY-NOTICES.md", "docs/dist/notes.md", "dist/notes.md", "x/go.sum",
                     "docs/pnpm-lock.yaml", "docs/package-lock.json", "docs/yarn.lock",
                     "docs/node_modules/notes.md", "agent/dist/notes.md", "frontend/distx/a.js"):
            with self.subTest(path=path):
                self.assertFalse(cps.is_text_skipped(path))
                scanner = cps.Scanner(checker())
                scanner.scan_file_bytes(path, f"{PRIVATE_IP} {REAL_SERIAL}\n".encode(), executable_bit=False)
                self.assertEqual({"private-ip", "serial-shape"}, {f.rule for f in scanner.findings})


class Modes(unittest.TestCase):
    def run_cli(self, *argv: str, stdin: str = "", cwd: Path | None = None) -> tuple[int, str, str]:
        proc = subprocess.run([sys.executable, str(HERE / "check_public_shape.py"), *argv], input=stdin,
                              capture_output=True, text=True, cwd=cwd)
        return proc.returncode, proc.stdout, proc.stderr

    def test_tree_mode_reports_path_line_rule(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "doc.md").write_text(f"ok line\nsee {LINUX_HOME}\n", encoding="utf-8")
            (root / "clean.md").write_text("example 192.0.2.1 a@example.com\n", encoding="utf-8")
            code, out, err = self.run_cli("--tree", tmp)
            self.assertEqual(1, code)
            self.assertEqual(["doc.md:2:home-path: home directory path naming a user"], out.splitlines())
            self.assertIn("1 finding(s)", err)

    def test_tree_mode_walks_node_modules_only_away_from_a_package(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "web" / "node_modules" / "dep").mkdir(parents=True)
            (root / "web" / "package.json").write_text("{}\n", encoding="utf-8")
            (root / "web" / "node_modules" / "dep" / "x.md").write_text(f"{PRIVATE_IP}\n", encoding="utf-8")
            (root / "docs" / "node_modules").mkdir(parents=True)
            (root / "docs" / "node_modules" / "notes.md").write_text(f"{PRIVATE_IP}\n", encoding="utf-8")
            code, out, _ = self.run_cli("--tree", tmp)
            self.assertEqual(1, code)
            self.assertEqual(["docs/node_modules/notes.md:1:private-ip"],
                             [line.rsplit(":", 1)[0] for line in out.splitlines()])

    def test_tree_mode_clean_exits_zero(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / "clean.md").write_text("nothing here\n", encoding="utf-8")
            code, out, _ = self.run_cli("--tree", tmp)
            self.assertEqual((0, ""), (code, out))

    def test_github_format(self) -> None:
        code, out, _ = self.run_cli("--stdin", "--format", "github", stdin=f"x {FINDING_ID}\n")
        self.assertEqual(1, code)
        self.assertTrue(out.startswith("::error file=stdin,line=1,title=id-token::"), out)

    def test_github_format_surfaces_opt_outs_as_warnings(self) -> None:
        line = f"see {LINUX_HOME}  # publication-guard: allow home-path\n"
        code, out, err = self.run_cli("--stdin", "--format", "github", stdin=line)
        self.assertEqual(0, code, out + err)
        self.assertIn("::warning file=stdin,line=1,title=opt-out::home-path", out)
        self.assertIn("::notice title=publication-guard::1 opt-out(s)", out)

    def test_misuse_exits_two(self) -> None:
        code, _, _ = self.run_cli("--tree", "/nonexistent/dir/for/this/test")
        self.assertEqual(2, code)
        code, _, _ = self.run_cli()
        self.assertEqual(2, code)

    def test_commit_and_range_modes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
                   "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"}

            def g(*a: str) -> None:
                subprocess.run(["git", "-C", tmp, *a], check=True, capture_output=True, env=env)

            g("init", "-q")
            (repo / "a.md").write_text("clean\n", encoding="utf-8")
            g("add", "a.md")
            g("commit", "-q", "-m", "first")
            (repo / "a.md").write_text(f"clean\nnode {REAL_SERIAL}\n", encoding="utf-8")
            blob = repo / "blob"
            blob.write_bytes(b"\x7fELF" + b"\x00" * 16)
            blob.chmod(blob.stat().st_mode | stat.S_IXUSR)
            g("add", "a.md", "blob")
            g("commit", "-q", "-m", f"add node, measured {DATED_MEASUREMENT[9:]}")

            code, out, _ = self.run_cli("--commit", "HEAD", "--repo", tmp)
            self.assertEqual(1, code)
            lines = out.splitlines()
            self.assertIn("a.md:2:serial-shape: 16-hex token with a device serial prefix", lines)
            self.assertIn("blob:0:executable-magic: ELF executable", lines)
            self.assertTrue(any(ln.startswith("commit:") and ":id-token:" in ln for ln in lines), lines)

            code, out, _ = self.run_cli("--range", "HEAD~1..HEAD", "--repo", tmp)
            self.assertEqual(1, code)
            code, out, _ = self.run_cli("--commit", "HEAD~1", "--repo", tmp)
            self.assertEqual((0, ""), (code, out))

            (repo / "b.md").write_text(f"{REAL_MAC}\n", encoding="utf-8")
            g("add", "b.md")
            code, out, _ = self.run_cli("--staged", "--repo", tmp)
            self.assertEqual(1, code)
            self.assertIn("b.md:1:mac-real-oui", out)

    def test_a_range_honours_the_allow_file_its_tip_commits(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
                   "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"}

            def g(*a: str) -> None:
                subprocess.run(["git", "-C", tmp, *a], check=True, capture_output=True, env=env)

            g("init", "-q")
            (repo / "a.md").write_text("clean\n", encoding="utf-8")
            g("add", "a.md")
            g("commit", "-q", "-m", "first")
            (repo / "gen.txt").write_text("a" * (cps.LARGE_FILE_BYTES + 1), encoding="utf-8")
            g("add", "gen.txt")
            g("commit", "-q", "-m", "second")
            # An allow file that is only in the working tree exempts nothing.
            (repo / cps.TREE_ALLOW_FILE).write_text("gen.txt large-file  # generated\n", encoding="utf-8")
            code, out, _ = self.run_cli("--range", "HEAD~1..HEAD", "--repo", tmp)
            self.assertEqual(1, code, out)
            self.assertIn("gen.txt:0:large-file", out)
            g("add", cps.TREE_ALLOW_FILE)
            g("commit", "-q", "-m", "third")
            code, out, _ = self.run_cli("--range", "HEAD~2..HEAD", "--repo", tmp)
            self.assertEqual(0, code, out)
            # The commit before the exemption existed still fails on its own.
            code, out, _ = self.run_cli("--commit", "HEAD~1", "--repo", tmp)
            self.assertEqual(1, code, out)

    def test_a_renamed_and_edited_file_is_scanned(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
                   "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"}

            def g(*a: str) -> None:
                subprocess.run(["git", "-C", tmp, *a], check=True, capture_output=True, env=env)

            g("init", "-q")
            body = "".join(f"line {i} of an ordinary file\n" for i in range(40))
            (repo / "a.txt").write_text(body, encoding="utf-8")
            g("add", "a.txt")
            g("commit", "-q", "-m", "first")
            g("mv", "a.txt", "b.txt")
            (repo / "b.txt").write_text(body + f"node {REAL_SERIAL}\n", encoding="utf-8")
            g("add", "b.txt")
            code, out, _ = self.run_cli("--staged", "--repo", tmp)
            self.assertEqual(1, code, out)
            self.assertIn("b.txt:41:serial-shape", out)
            g("commit", "-q", "-m", "rename")
            for mode in (("--commit", "HEAD"), ("--range", "HEAD~1..HEAD")):
                code, out, _ = self.run_cli(*mode, "--repo", tmp)
                self.assertEqual(1, code, f"{mode}: {out}")
                self.assertIn("b.txt:41:serial-shape", out)

    def test_files_mode_reads_list_from_stdin(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / "x.txt").write_text(f"{CGNAT_IP}\n", encoding="utf-8")
            (Path(tmp) / "y.txt").write_text("fine\n", encoding="utf-8")
            code, out, _ = self.run_cli("--files", "-", "--root", tmp, stdin="x.txt\ny.txt\n")
            self.assertEqual(1, code)
            self.assertEqual(["x.txt:1:private-ip: CGNAT address outside the generic example subnets"], out.splitlines())


def git_repo(tmp: str):
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
           "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
           "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}

    def g(*a: str) -> None:
        subprocess.run(["git", "-C", tmp, *a], check=True, capture_output=True, env=env)

    g("init", "-q")
    return g


class GitAttributes(unittest.TestCase):
    """A `.gitattributes` that blinds a diff-based scan is a finding, in any
    directory and in every mode; the diff modes cannot be blinded by one."""

    BLINDING = ("* -diff", "*.md binary", "* diff=conv", "*.c filter=lfs", "docs/ export-ignore", "VERSION export-subst")
    HARMLESS = ("*.sh text eol=lf", "*.md diff", "x !diff", "# * -diff in a comment")

    def run_cli(self, *argv: str) -> tuple[int, str, str]:
        proc = subprocess.run([sys.executable, str(HERE / "check_public_shape.py"), *argv],
                              capture_output=True, text=True)
        return proc.returncode, proc.stdout, proc.stderr

    def test_rule_in_tree_mode(self) -> None:
        for attrs in self.BLINDING:
            with self.subTest(attrs=attrs), tempfile.TemporaryDirectory() as tmp:
                (Path(tmp) / "sub").mkdir()
                (Path(tmp) / "sub" / ".gitattributes").write_text(f"*.sh text\n{attrs}\n", encoding="utf-8")
                code, out, _ = self.run_cli("--tree", tmp)
                self.assertEqual(1, code, out)
                self.assertEqual(["sub/.gitattributes:2:gitattributes"], [ln.rsplit(":", 1)[0] for ln in out.splitlines()])
        for attrs in self.HARMLESS:
            with self.subTest(attrs=attrs), tempfile.TemporaryDirectory() as tmp:
                (Path(tmp) / ".gitattributes").write_text(attrs + "\n", encoding="utf-8")
                code, out, _ = self.run_cli("--tree", tmp)
                self.assertEqual((0, ""), (code, out))

    def test_rule_cannot_be_opted_out(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / ".gitattributes").write_text("* -diff # publication-guard: allow gitattributes\n",
                                                     encoding="utf-8")
            code, out, _ = self.run_cli("--tree", tmp)
            self.assertEqual(1, code, out)
            (Path(tmp) / ".gitattributes").write_text("* -diff\n", encoding="utf-8")
            (Path(tmp) / cps.TREE_ALLOW_FILE).write_text(".gitattributes gitattributes  # please\n", encoding="utf-8")
            code, _, err = self.run_cli("--tree", tmp)
            self.assertEqual(2, code, err)
            self.assertIn("cannot be silenced", err)

    def test_commit_staged_and_range_modes_see_through_the_attributes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            g = git_repo(tmp)
            repo = Path(tmp)
            (repo / "a.md").write_text("clean\n", encoding="utf-8")
            g("add", "a.md")
            g("commit", "-q", "-m", "first")
            (repo / ".gitattributes").write_text("* -diff\n", encoding="utf-8")
            g("add", ".gitattributes")
            code, out, _ = self.run_cli("--staged", "--repo", tmp)
            self.assertEqual(1, code, out)
            self.assertIn(".gitattributes:1:gitattributes", out)
            g("commit", "-q", "-m", "attributes")
            (repo / "b.md").write_text(f"node {REAL_SERIAL}\n", encoding="utf-8")
            g("add", "b.md")
            code, out, _ = self.run_cli("--staged", "--repo", tmp)
            self.assertEqual(1, code, out)
            self.assertIn("b.md:1:serial-shape", out)
            g("commit", "-q", "-m", "add")
            g("rm", "-q", "b.md")
            g("commit", "-q", "-m", "remove")
            code, out, _ = self.run_cli("--range", "HEAD~3..HEAD", "--repo", tmp)
            self.assertEqual(1, code, out)
            self.assertIn("b.md:1:serial-shape", out)
            self.assertIn(".gitattributes:1:gitattributes", out)


class TreeAllowFile(unittest.TestCase):
    """`.publication-guard-allow` at the tree root: path + rule + reason."""

    def run_cli(self, *argv: str) -> tuple[int, str, str]:
        proc = subprocess.run([sys.executable, str(HERE / "check_public_shape.py"), *argv],
                              capture_output=True, text=True)
        return proc.returncode, proc.stdout, proc.stderr

    def tree(self, tmp: str) -> Path:
        root = Path(tmp)
        (root / "bin").mkdir()
        (root / "bin" / "tool").write_bytes(b"\x7fELF" + b"\x00" * 16)
        (root / "NOTICES.txt").write_text(f"Copyright someone <{PERSON_EMAIL}>\n", encoding="utf-8")
        return root

    def test_entries_silence_and_are_counted(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = self.tree(tmp)
            code, out, _ = self.run_cli("--tree", tmp)
            self.assertEqual(1, code)
            (root / cps.TREE_ALLOW_FILE).write_text(
                "# comment line\n"
                "bin/tool executable-magic  # a prebuilt upstream binary\n"
                "NOTICES.txt email-domain   # an upstream notice reproduced unaltered\n", encoding="utf-8")
            code, out, err = self.run_cli("--tree", tmp)
            self.assertEqual((0, ""), (code, out), err)
            self.assertIn("2 opt-out(s)", err)
            self.assertIn("opt-out bin/tool:0 executable-magic", err)

    def test_an_entry_silences_only_its_path_and_rule(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = self.tree(tmp)
            (root / "other.txt").write_text(f"mail {PERSON_EMAIL}\n", encoding="utf-8")
            (root / cps.TREE_ALLOW_FILE).write_text(
                "bin/tool executable-magic  # upstream binary\n"
                "NOTICES.txt email-domain   # upstream notice\n", encoding="utf-8")
            code, out, _ = self.run_cli("--tree", tmp)
            self.assertEqual(1, code)
            self.assertEqual(["other.txt:1:email-domain"], [ln.rsplit(":", 1)[0] for ln in out.splitlines()])

    def test_missing_reason_unknown_rule_and_stale_entry_are_refused(self) -> None:
        for body, needle in (("bin/tool executable-magic\n", "reason"),
                             ("bin/tool no-such-rule  # why\n", "unknown rule"),
                             ("bin/tool executable-magic  # x\nNOTICES.txt email-domain  # x\n"
                              "gone.txt large-file  # x\n", "silences nothing")):
            with self.subTest(needle=needle), tempfile.TemporaryDirectory() as tmp:
                root = self.tree(tmp)
                (root / cps.TREE_ALLOW_FILE).write_text(body, encoding="utf-8")
                code, _, err = self.run_cli("--tree", tmp)
                self.assertEqual(2, code, err)
                self.assertIn(needle, err)

    def test_a_worktree_git_file_is_not_content(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / ".git").write_text(f"gitdir: {MAC_HOME}/repo/.git/worktrees/x\n", encoding="utf-8")
            code, out, _ = self.run_cli("--tree", tmp)
            self.assertEqual((0, ""), (code, out))


class OwnDirectoryIsClean(unittest.TestCase):
    def test_checker_passes_on_its_own_directory(self) -> None:
        """The tool ships in the public tree, so its own sources must pass it."""
        buf_out, buf_err = io.StringIO(), io.StringIO()
        code = cps.run(["--tree", str(HERE)], out=buf_out, err=buf_err)
        self.assertEqual(0, code, buf_out.getvalue())


if __name__ == "__main__":
    unittest.main()
