#!/usr/bin/env python3
"""Tests for check_workflows.py."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_workflows as cw  # noqa: E402

CLEAN = """name: CI
on:
  push:
    branches: [main]
  pull_request:
jobs:
  a:
    runs-on: ubuntu-24.04
    steps: []
  b:
    runs-on: ${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '["ubuntu-24.04"]') }}
    steps: []
  c:
    strategy:
      matrix:
        include:
          - runner: ubuntu-24.04-arm
    runs-on: ${{ matrix.runner }}
    steps: []
"""


# A guard workflow; each PATHS marker is where a test may insert a filter.
GUARD = """name: Publication guard
on:
  push:
    branches: [main]
PATHS
  pull_request:
    branches: [main]
PATHS
  merge_group:
jobs:
  publication-guard:
    runs-on: ubuntu-24.04
    steps: []
"""


# The public-job fixtures, IDENTICAL in
# tools/publication-guard/check_workflows_test.py and adversarialPublic in
# backend/pkg/repoguard/workflow_fork_guard_test.go: change both or neither.
# (name, `if:` as YAML, `runs-on:` as YAML, fork guard accepts, public checker accepts)
#
# The fork guard judges the `if:`; the public checker has no `if:`
# concept (in the public repository EVERY job must be hosted), so it judges
# `runs-on:` alone. That is why `gate && (a) || (b)` on the exact
# PUBLIC_CI_RUNS_ON expression differs: the `||` escapes the gate, so the
# guard does not count the job as public and judges it as a self-hosted job
# with no fork guard; for the public checker the runner is the hosted
# expression either way, and the `if:` decides only WHETHER it runs.
ADVERSARIAL_PUBLIC = [
    (
        "A1 a mention with ||, a string-literal runner",
        "vars.PUBLIC_CI_ENABLED == 'true' || github.event_name == 'pull_request'",
        "${{ 'example-arm64' }}",
        False,
        False,
    ),
    (
        "A2 a string-literal gate, a format() runner",
        "\"'vars.PUBLIC_CI_ENABLED' != ''\"",
        "${{ format('{0}-arm64','example') }}",
        False,
        False,
    ),
    ("exact gate, a string-literal runner", "vars.PUBLIC_CI_ENABLED == 'true'", "${{ 'example-arm64' }}", False, False),
    (
        "exact gate, a non-hosted fallback",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[\"example-arm64\"]') }}",
        False,
        False,
    ),
    (
        "exact gate, an empty fallback",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[]') }}",
        False,
        False,
    ),
    (
        "exact gate, another variable",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.OTHER_RUNS_ON || '[\"ubuntu-24.04\"]') }}",
        False,
        False,
    ),
    (
        "exact gate, an || after fromJSON",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[\"ubuntu-24.04\"]') || 'example-arm64' }}",
        False,
        False,
    ),
    (
        "exact gate, the exact expression",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[\"ubuntu-24.04\"]') }}",
        True,
        True,
    ),
    (
        "exact gate, the per-architecture variable",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON_ARM64 || '[\"ubuntu-24.04-arm\"]') }}",
        True,
        True,
    ),
    (
        "exact gate, the per-architecture variable, a non-hosted fallback",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON_AMD64 || '[\"example-arm64\"]') }}",
        False,
        False,
    ),
    (
        "exact gate, a look-alike suffix",
        "vars.PUBLIC_CI_ENABLED == 'true'",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON_X || '[\"ubuntu-24.04\"]') }}",
        False,
        False,
    ),
    (
        "gate and another variable, a hosted literal",
        "vars.PUBLIC_CI_ENABLED == 'true' && vars.Y == 'x'",
        "ubuntu-24.04",
        True,
        True,
    ),
    ("exact gate, a hosted literal list", "vars.PUBLIC_CI_ENABLED == 'true'", "[ubuntu-24.04, ubuntu-24.04-arm]", True, True),
    (
        "the guard.yml shape",
        "vars.PUBLIC_CI_ENABLED == 'true' && (github.event_name == 'push' || github.event_name == 'merge_group')",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[\"ubuntu-24.04\"]') }}",
        True,
        True,
    ),
    (
        "gate && (a) || (b)",
        "vars.PUBLIC_CI_ENABLED == 'true' && (github.event_name == 'push') || (github.event_name == 'pull_request')",
        "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[\"ubuntu-24.04\"]') }}",
        False,
        True,
    ),
]


def public_fixture(cond: str, runs_on: str) -> str:
    return f"on: [pull_request]\njobs:\n  a:\n    if: {cond}\n    runs-on: {runs_on}\n    steps: []\n"


class WorkflowCheckTest(unittest.TestCase):
    def test_clean(self) -> None:
        self.assertEqual(cw.check_text("w.yml", CLEAN), [])

    def test_self_hosted_label(self) -> None:
        text = CLEAN.replace("runs-on: ubuntu-24.04\n", "runs-on: [self-hosted, lab-box]\n")
        found = cw.check_text("w.yml", text)
        self.assertTrue(any("workflow-runner" in f for f in found), found)

    def test_unknown_runner_name(self) -> None:
        text = CLEAN.replace("runs-on: ubuntu-24.04\n", "runs-on: my-own-box\n")
        found = cw.check_text("w.yml", text)
        self.assertEqual(len(found), 1)
        self.assertIn("not a GitHub-hosted", found[0])

    def test_matrix_runner_label(self) -> None:
        text = CLEAN.replace("- runner: ubuntu-24.04-arm", "- runner: [self-hosted, arm-box]")
        found = cw.check_text("w.yml", text)
        self.assertTrue(found)

    # The hosted-runner rule is the fork guard's (HOSTED_RUNNER, equal to
    # hostedRunnerRe in backend/pkg/repoguard): a job is hosted only if
    # EVERY label is a GitHub-hosted one. Each fixture names a self-hosted
    # machine in a shape the older, looser pattern let through or never read.
    ADVERSARIAL = {
        "scalar": "    runs-on: example-arm64\n",
        "flow-list": "    runs-on: [linux, example-arm64]\n",
        "look-alike": "    runs-on: ubuntu-example-arm64\n",
        "block-list": "    runs-on:\n      - linux\n      - example-arm64\n",
        "flow-mapping": "    runs-on: {group: lab}\n",
        "block-mapping": "    runs-on:\n      group: lab\n      labels: example-arm64\n",
        "expression-fallback": "    runs-on: ${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[\"example-arm64\"]') }}\n",
        "matrix-named-key": (
            "    strategy:\n      matrix:\n        os: [ubuntu-24.04, example-arm64]\n    runs-on: ${{ matrix.os }}\n"
        ),
        "matrix-include": (
            "    strategy:\n      matrix:\n        include:\n          - runner: example-arm64\n"
            "    runs-on: ${{ matrix.runner }}\n"
        ),
        "matrix-block-list": (
            "    strategy:\n      matrix:\n        runner:\n          - ubuntu-24.04\n          - example-arm64\n"
            "    runs-on: ${{ matrix.runner }}\n"
        ),
    }

    def test_adversarial_runner_shapes_are_refused(self) -> None:
        for name, runs_on in self.ADVERSARIAL.items():
            text = CLEAN.replace("    runs-on: ubuntu-24.04\n", runs_on)
            found = cw.check_text("w.yml", text)
            self.assertTrue(any(":workflow-runner:" in f for f in found), (name, found))

    def test_hosted_variants_are_accepted(self) -> None:
        for label in ("ubuntu-latest", "ubuntu-24.04-arm", "ubuntu-22.04-4-cores", "windows-2022",
                      "windows-11-arm", "macos-14-xlarge", "macos-latest", "Ubuntu-Latest"):
            text = CLEAN.replace("runs-on: ubuntu-24.04\n", f"runs-on: {label}\n")
            self.assertEqual(cw.check_text("w.yml", text), [], label)
        text = CLEAN.replace("    runs-on: ubuntu-24.04\n", "    runs-on:\n      - ubuntu-24.04\n")
        self.assertEqual(cw.check_text("w.yml", text), [])

    def test_a_matrix_name_outside_the_matrix_is_not_a_label(self) -> None:
        text = CLEAN.replace("${{ matrix.runner }}", "${{ matrix.os }}").replace(
            "- runner: ubuntu-24.04-arm", "- os: ubuntu-24.04-arm").replace(
            "    steps: []\n", "    steps:\n      - with:\n          os: anything\n", 1)
        self.assertEqual(cw.check_text("w.yml", text), [])

    def test_forbidden_triggers(self) -> None:
        for trig in ("pull_request_target", "workflow_run"):
            text = CLEAN.replace("  pull_request:\n", f"  {trig}:\n")
            found = cw.check_text("w.yml", text)
            self.assertTrue(any("workflow-trigger" in f for f in found), (trig, found))

    def test_forbidden_triggers_in_every_shape(self) -> None:
        # A line pattern sees only the block-mapping key; each of these shapes
        # must still be a finding.
        body = "jobs:\n  a:\n    runs-on: ubuntu-24.04\n    steps: []\n"
        shapes = {
            "scalar": "on: pull_request_target\n",
            "flow-list": "on: [push, workflow_run]\n",
            "flow-mapping": "on: {pull_request_target: {}}\n",
            "quoted-key": 'on:\n  "pull_request_target":\n',
            "single-quoted-key": "on:\n  'workflow_run':\n    workflows: [x]\n",
        }
        for name, on in shapes.items():
            found = cw.check_text("w.yml", on + body)
            self.assertTrue(any(":workflow-trigger:" in f for f in found), (name, found))

    def test_a_file_the_reader_refuses_is_a_finding(self) -> None:
        text = "on: &anchor [push]\njobs:\n  a:\n    runs-on: ubuntu-24.04\n"
        found = cw.check_text("w.yml", text)
        self.assertTrue(any(":workflow-parse:" in f for f in found), found)

    def test_public_jobs(self) -> None:
        # Any expression but the exact PUBLIC_CI_RUNS_ON form (or a bare
        # matrix reference) is refused, `${{ 'example-arm64' }}` included.
        for name, cond, runs_on, _, public_ok in ADVERSARIAL_PUBLIC:
            with self.subTest(name):
                found = cw.check_text("w.yml", public_fixture(cond, runs_on))
                self.assertEqual(found == [], public_ok, found)

    def test_an_inline_matrix_expression_is_refused(self) -> None:
        text = CLEAN.replace(
            "      matrix:\n        include:\n          - runner: ubuntu-24.04-arm\n",
            "      matrix: ${{ fromJSON(vars.MATRIX) }}\n",
        )
        found = cw.check_text("w.yml", text)
        self.assertTrue(any("inline `matrix:`" in f for f in found), found)

    def test_guard_workflow_without_path_filter_is_clean(self) -> None:
        text = GUARD.replace("PATHS\n", "")
        self.assertEqual(cw.check_text("guard.yml", text), [])

    def test_guard_workflow_with_a_path_filter_is_refused(self) -> None:
        for event in ("push", "pull_request"):
            for key in ("paths", "paths-ignore"):
                filt = f"    {key}:\n      - 'docs/**'\n"
                text = GUARD.replace(f"  {event}:\n    branches: [main]\nPATHS\n", f"  {event}:\n    branches: [main]\n{filt}")
                text = text.replace("PATHS\n", "")
                found = cw.check_text("guard.yml", text)
                self.assertTrue(any(":guard-path-filter:" in f for f in found), (event, key, found))

    def test_private_identifier_guard_job_counts_too(self) -> None:
        text = GUARD.replace("  publication-guard:", "  private-identifier-guard:").replace(
            "PATHS\n", "    paths-ignore: ['**/*.md']\n", 1).replace("PATHS\n", "")
        found = cw.check_text("guard.yml", text)
        self.assertTrue(any(":guard-path-filter:" in f for f in found), found)

    def test_path_filter_without_a_guard_job_is_allowed(self) -> None:
        text = GUARD.replace("  publication-guard:", "  build:").replace(
            "PATHS\n", "    paths-ignore: ['**/*.md']\n", 1).replace("PATHS\n", "")
        self.assertEqual(cw.check_text("ci.yml", text), [])

    def test_a_step_named_like_the_guard_is_not_a_job(self) -> None:
        text = GUARD.replace("  publication-guard:", "  build:").replace(
            "steps: []", "steps:\n      - name: publication-guard\n        run: true").replace(
            "PATHS\n", "    paths-ignore: ['**/*.md']\n", 1).replace("PATHS\n", "")
        self.assertEqual(cw.check_text("ci.yml", text), [])

    def test_comment_is_ignored(self) -> None:
        text = CLEAN + "# runs-on: [self-hosted, nothing] — a comment is not a job\n"
        self.assertEqual(cw.check_text("w.yml", text), [])


if __name__ == "__main__":
    unittest.main()
