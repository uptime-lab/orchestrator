package repoguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// workflowsDir is .github/workflows relative to this package.
const workflowsDir = "../../../.github/workflows"

// The fork guard, byte for byte as the workflow files carry it. The two halves
// are also checked one at a time so a failure names which half is missing.
//
//   - eventNameHalf is load-bearing: on push, schedule and workflow_dispatch
//     runs `head.repo.full_name` is empty, so a bare `==` would skip every
//     push run on main and starve a CI watchdog that reads the ABSENCE of a
//     Tests run on main as its alarm.
//   - sameRepoHalf is what the guard is for: a pull_request run from a fork
//     evaluates to false and the job is skipped instead of running the PR's
//     code on a self-hosted machine.
//
// The guard is defence in depth, not the boundary (a hostile fork PR edits its
// own copy of the workflow); the boundary is the owner-side GitHub setting that
// requires approval before a fork's workflow runs. What this test pins is that
// a collaborator's slip in those settings turns into a skipped job on EVERY
// self-hosted pull_request job, not on every job but one — which is how a job
// without the guard once shipped.
const (
	eventNameHalf = "github.event_name != 'pull_request'"
	sameRepoHalf  = "github.event.pull_request.head.repo.full_name == github.repository"
	forkGuard     = eventNameHalf + " || " + sameRepoHalf
)

// workflow is the slice of a GitHub Actions workflow file this guard reads.
// `on` and `runs-on` take several YAML shapes (scalar, sequence, mapping /
// expression), so both stay yaml.Node and are interpreted below.
type workflow struct {
	On   yaml.Node        `yaml:"on"`
	Jobs map[string]wfJob `yaml:"jobs"`
}

type wfJob struct {
	RunsOn yaml.Node `yaml:"runs-on"`
	If     string    `yaml:"if"`
}

// triggers returns the event names a workflow's `on` declares, whichever of
// the three shapes GitHub accepts it uses: `on: push`, `on: [push,
// pull_request]`, or the mapping form with per-event filters.
func triggers(t *testing.T, file string, on yaml.Node) []string {
	t.Helper()
	switch on.Kind {
	case yaml.ScalarNode:
		return []string{on.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(on.Content))
		for _, n := range on.Content {
			out = append(out, n.Value)
		}
		return out
	case yaml.MappingNode:
		out := make([]string, 0, len(on.Content)/2)
		for i := 0; i < len(on.Content); i += 2 {
			out = append(out, on.Content[i].Value)
		}
		return out
	case 0:
		t.Fatalf("%s: no `on:` block — every workflow here declares its triggers", file)
	}
	t.Fatalf("%s: unexpected YAML shape for `on:` (kind %d)", file, on.Kind)
	return nil
}

// runsOnLabels flattens `runs-on` to its labels. A scalar or a sequence is the
// literal label set; a mapping (`group:`/`labels:`) or an expression
// (`${{ matrix.runner }}`) cannot be read statically and is returned as a
// single label that the caller treats as self-hosted — fail closed, because
// an expression can resolve to a self-hosted runner.
func runsOnLabels(t *testing.T, file, job string, runsOn yaml.Node) []string {
	t.Helper()
	switch runsOn.Kind {
	case yaml.ScalarNode:
		return []string{runsOn.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(runsOn.Content))
		for _, n := range runsOn.Content {
			out = append(out, n.Value)
		}
		return out
	case yaml.MappingNode:
		return []string{"self-hosted"}
	case 0:
		t.Fatalf("%s: job %q has no `runs-on:`", file, job)
	}
	t.Fatalf("%s: job %q has an unexpected YAML shape for `runs-on:` (kind %d)", file, job, runsOn.Kind)
	return nil
}

// isSelfHosted is true unless EVERY label is a GitHub-hosted one. GitHub
// routes a label it does not host to self-hosted runners whether or not
// `self-hosted` is listed, so `runs-on: example-arm64` is a self-hosted job;
// a criterion on the literal `self-hosted` label let such a job through with
// no guard at all. An expression cannot be read statically and is outside
// the hosted set, so it counts as self-hosted — fail closed — and so does an
// empty label set.
func isSelfHosted(labels []string) bool {
	if len(labels) == 0 {
		return true
	}
	for _, l := range labels {
		if !hostedRunnerRe.MatchString(l) {
			return true
		}
	}
	return false
}

// guardProblem judges one job's `if:` and returns "" when it is the fork
// guard, else a sentence naming what is wrong. The halves are checked one at
// a time before the byte comparison so the failure says which half went.
// unwrapVariableGate strips a leading variable gate, `vars.A == '…' &&
// vars.B == '…' && ( … )`: a workflow that is switched on by repository
// variables still has to carry the fork guard, and the guard is then the
// parenthesised remainder. The WHOLE condition must have that shape
// (gatedGuardRe): the prefix is validated, never skipped to the first `&& (`,
// because `vars.X == 'true' || vars.Y == 'z' && (guard)` is true on a fork PR
// whenever X is set. A condition that starts with `vars.` in any other shape
// reports ok=false.
func unwrapVariableGate(cond string) (string, bool) {
	trimmed := strings.TrimSpace(cond)
	if !strings.HasPrefix(trimmed, "vars.") {
		return cond, true
	}
	m := gatedGuardRe.FindStringSubmatch(trimmed)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// gatedGuardRe is one or more `vars.NAME == '...'` terms joined by `&&`, then
// `&& (` the guard `)`.
var gatedGuardRe = regexp.MustCompile(`(?s)^vars\.[A-Za-z_][A-Za-z0-9_]*\s*==\s*'[^']*'(?:\s*&&\s*vars\.[A-Za-z_][A-Za-z0-9_]*\s*==\s*'[^']*')*\s*&&\s*\((.*)\)$`)

func guardProblem(cond string) string {
	cond, ok := unwrapVariableGate(cond)
	if !ok {
		return "has a variable gate that is not exactly `vars.NAME == '...'` terms joined by `&&`, then `&& (<guard>)` — any other term in front of the guard (an `||`, a `!=`, a function) can make the job run on a fork PR"
	}
	cond = strings.TrimSpace(cond)
	switch {
	case cond == "":
		return "runs self-hosted on pull_request and has NO `if:` — a fork PR would run its code on the self-hosted runners"
	case !strings.Contains(cond, eventNameHalf):
		return "lacks the `" + eventNameHalf + "` half — push/schedule/dispatch runs would be skipped and the CI watch would fire"
	case !strings.Contains(cond, sameRepoHalf):
		return "lacks the `" + sameRepoHalf + "` half — a fork PR would run its code on the self-hosted runners"
	case cond != forkGuard:
		return "carries both halves but not the exact guard; keep the one spelling so the check stays a byte comparison:\n\twant: " + forkGuard + "\n\tgot:  " + cond
	}
	return ""
}

func loadWorkflows(t *testing.T) map[string]workflow {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(workflowsDir, "*.yml"))
	if err != nil {
		t.Fatalf("globbing the workflows: %v", err)
	}
	more, err := filepath.Glob(filepath.Join(workflowsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("globbing the workflows: %v", err)
	}
	files = append(files, more...)
	if len(files) == 0 {
		if _, statErr := os.Stat(workflowsDir); os.IsNotExist(statErr) {
			// A build from backend/ alone has no workflows directory. From a
			// full checkout the test runs; CI runs it there.
			t.Skipf(".github/workflows not present in this build context")
		}
		t.Fatalf("no workflow files under %s; if they moved, this test moves with them", workflowsDir)
	}

	out := make(map[string]workflow, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		var wf workflow
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("parsing %s: %v", f, err)
		}
		out[filepath.Base(f)] = wf
	}
	return out
}

// hostedRunnerRe is a GitHub-hosted runner label, as actionlint 1.7.12 knows
// them (its runner-label rule): the -latest and version forms, the arm,
// larger-runner and core-count variants, matched case-insensitively as GitHub
// matches labels. Any other label is routed to self-hosted runners. Kept
// equal to HOSTED_RUNNER in tools/publication-guard/check_workflows.py.
var hostedRunnerRe = regexp.MustCompile(`(?i)^(?:ubuntu-(?:latest|slim|[0-9]{2}\.[0-9]{2})(?:-arm)?(?:-[0-9]+-cores)?|windows-(?:latest|[0-9]{4}|11-arm)(?:-vs[0-9]{4}|-[0-9]+-cores)?|macos-(?:latest|[0-9]{2})(?:-large|-xlarge|-xl)?)$`)

// publicGateRe is a PUBLIC job's `if:`: exactly `vars.PUBLIC_CI_ENABLED ==
// 'true'`, optionally &&-joined with other `vars.NAME == '...'` terms on
// either side, optionally followed by ONE final `&& ( ... )` group (guard.yml
// narrows its private-identifier-guard job to push and merge_group that way). Every extra
// term is a conjunct and only narrows. Anything else — an `||`, a function, a
// string literal that merely MENTIONS the variable — is not a gate, and the
// job is judged as a self-hosted one. The final group must be ONE group
// (balancedGroup): `&& (a) || (b)` matches with group `a) || (b`.
var publicGateRe = regexp.MustCompile(`(?s)^(?:vars\.[A-Za-z_][A-Za-z0-9_]*\s*==\s*'[^']*'\s*&&\s*)*vars\.PUBLIC_CI_ENABLED\s*==\s*'true'(?:\s*&&\s*vars\.[A-Za-z_][A-Za-z0-9_]*\s*==\s*'[^']*')*(?:\s*&&\s*\((.*)\))?$`)

// publicRunsOnRe is the one expression a public job may run on: the
// PUBLIC_CI_RUNS_ON variable (a repository setting a fork cannot change) with
// a literal JSON fallback, every label of which must be hosted. Any other
// expression (a string literal, format(), another variable) can name a
// self-hosted machine. Kept equal to PUBLIC_RUNS_ON in
// tools/publication-guard/check_workflows.py.
var publicRunsOnRe = regexp.MustCompile(`^\$\{\{\s*fromJSON\(\s*vars\.PUBLIC_CI_RUNS_ON(?:_AMD64|_ARM64)?\s*\|\|\s*'(\[[^']*\])'\s*\)\s*\}\}$`)

// balancedGroup is true when text never closes more parentheses than it
// opened, outside single-quoted expression strings.
func balancedGroup(text string) bool {
	depth, quote := 0, false
	for _, c := range text {
		switch {
		case c == '\'':
			quote = !quote
		case quote:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !quote
}

// isPublicGate: cond is publicGateRe and its final group, if any, is one group.
func isPublicGate(cond string) bool {
	trimmed := strings.TrimSpace(cond)
	m := publicGateRe.FindStringSubmatchIndex(trimmed)
	if m == nil {
		return false
	}
	return m[2] < 0 || balancedGroup(trimmed[m[2]:m[3]])
}

// hostedFallback: expr is exactly publicRunsOnRe and its fallback is a
// non-empty JSON list of hosted labels.
func hostedFallback(expr string) bool {
	m := publicRunsOnRe.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return false
	}
	var items []any
	if err := json.Unmarshal([]byte(m[1]), &items); err != nil || len(items) == 0 {
		return false
	}
	for _, i := range items {
		s, ok := i.(string)
		if !ok || !hostedRunnerRe.MatchString(s) {
			return false
		}
	}
	return true
}

// publicRunnerProblem judges a PUBLIC job (isPublicGate). Such a job carries
// no fork guard -- a fork PR is meant to run it, on a hosted runner -- so the
// one thing that must hold is that it runs only on hosted labels: literal
// hosted names, or the single publicRunsOnRe expression with a hosted
// fallback. Returns "" when that holds.
func publicRunnerProblem(labels []string) string {
	if len(labels) == 0 {
		return "is a public (variable-gated) job with an empty `runs-on:`"
	}
	if len(labels) == 1 && strings.Contains(labels[0], "${{") {
		if hostedFallback(labels[0]) {
			return ""
		}
		return fmt.Sprintf("is a public (variable-gated) job but runs on the expression %q — the only expression a public job may run on is `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[<hosted labels>]') }}`; any other can name a self-hosted machine", labels[0])
	}
	for _, l := range labels {
		if hostedRunnerRe.MatchString(l) {
			continue
		}
		return fmt.Sprintf("is a public (variable-gated) job but runs on the literal label %q — a public job runs on GitHub-hosted image names or the PUBLIC_CI_RUNS_ON expression, never a named machine", l)
	}
	return ""
}

// forkGuardReport is what checkForkGuard found across a set of workflows.
type forkGuardReport struct {
	selfHosted int // self-hosted pull_request jobs checked for the fork guard
	public     int // variable-gated public pull_request jobs checked for their runner
	failures   []string
}

// checkForkGuard applies both rules to every pull_request-triggered job.
func checkForkGuard(t *testing.T, workflows map[string]workflow) forkGuardReport {
	t.Helper()
	var r forkGuardReport
	files := make([]string, 0, len(workflows))
	for f := range workflows {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, file := range files {
		wf := workflows[file]
		hasPR := false
		for _, ev := range triggers(t, file, wf.On) {
			if ev == "pull_request" {
				hasPR = true
			}
		}
		if !hasPR {
			continue
		}
		if len(wf.Jobs) == 0 {
			t.Fatalf("%s: triggers on pull_request but declares no jobs", file)
		}

		jobs := make([]string, 0, len(wf.Jobs))
		for name := range wf.Jobs {
			jobs = append(jobs, name)
		}
		sort.Strings(jobs)

		for _, name := range jobs {
			job := wf.Jobs[name]
			labels := runsOnLabels(t, file, name, job.RunsOn)
			// The public workflows are gated by a repository variable and
			// place their jobs through another; they carry no fork guard by
			// design. What must still hold is that the runner is never a
			// literal self-hosted label (tools/publication-guard/check_workflows.py
			// asserts the same line by line). PUBLIC_CI_RUNS_ON itself is a
			// repository setting no file can check: it must never name a
			// self-hosted label on a public repository.
			// Public only when the `if:` IS the gate (isPublicGate), not when it
			// merely mentions the variable: `gate || x`, or a string literal
			// naming the variable, once exempted any expression runner.
			if isPublicGate(job.If) {
				r.public++
				if problem := publicRunnerProblem(labels); problem != "" {
					r.failures = append(r.failures, fmt.Sprintf("%s: job %q %s", file, name, problem))
				}
				continue
			}
			if !isSelfHosted(labels) {
				continue
			}
			r.selfHosted++
			if problem := guardProblem(job.If); problem != "" {
				r.failures = append(r.failures, fmt.Sprintf("%s: job %q %s", file, name, problem))
			}
		}
	}
	return r
}

// TestEverySelfHostedPullRequestJobCarriesTheForkGuard: for every workflow
// that triggers on pull_request, every job that runs on a self-hosted runner
// must carry the fork guard, exactly, and every public (variable-gated) job
// must run on an expression or a hosted image. Written red against a job that
// had no `if:` at all.
//
// The test reads whichever tree it is in: a tree that uses self-hosted runners
// has guarded self-hosted pull_request jobs, the public repository has only
// gated public jobs. Finding NEITHER shape means it is reading the wrong thing.
func TestEverySelfHostedPullRequestJobCarriesTheForkGuard(t *testing.T) {
	r := checkForkGuard(t, loadWorkflows(t))
	if r.selfHosted == 0 && r.public == 0 {
		t.Fatalf("no self-hosted pull_request job and no variable-gated public job found in any workflow — this guard is reading the wrong thing")
	}
	for _, f := range r.failures {
		t.Error(f)
	}
}

// TestCheckForkGuardShapes pins the two repository shapes and one case that
// must never pass: a gated public job rewritten to a literal self-hosted label must fail.
func TestCheckForkGuardShapes(t *testing.T) {
	parse := func(t *testing.T, files map[string]string) map[string]workflow {
		t.Helper()
		out := map[string]workflow{}
		for name, text := range files {
			var wf workflow
			if err := yaml.Unmarshal([]byte(text), &wf); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			out[name] = wf
		}
		return out
	}
	const gate = "vars.PUBLIC_CI_ENABLED == 'true'"
	cases := []struct {
		name         string
		files        map[string]string
		selfHosted   int
		public       int
		wantFailures []string
	}{
		{
			name: "public repository: gated jobs only",
			files: map[string]string{"ci.yml": `on: {pull_request: {}, push: {}}
jobs:
  a: {if: "` + gate + `", runs-on: "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[\"ubuntu-24.04\"]') }}"}
  b: {if: "` + gate + `", runs-on: ubuntu-24.04}
`},
			public: 2,
		},
		{
			name: "self-hosted repository: a guarded self-hosted job",
			files: map[string]string{"selfhosted-ci.yml": `on: [pull_request]
jobs:
  a: {if: "` + forkGuard + `", runs-on: [self-hosted, example-arm64]}
`},
			selfHosted: 1,
		},
		{
			name: "gated public job on a literal self-hosted label",
			files: map[string]string{"ci.yml": `on: [pull_request]
jobs:
  a: {if: "` + gate + `", runs-on: [self-hosted, example-arm64]}
`},
			public:       1,
			wantFailures: []string{`literal label "self-hosted"`},
		},
		{
			name: "gated public job on a named machine",
			files: map[string]string{"ci.yml": `on: [pull_request]
jobs:
  a: {if: "` + gate + `", runs-on: example-amd64}
`},
			public:       1,
			wantFailures: []string{`literal label "example-amd64"`},
		},
		{
			name: "neither shape",
			files: map[string]string{"x.yml": `on: [pull_request]
jobs:
  a: {runs-on: ubuntu-24.04}
`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := checkForkGuard(t, parse(t, tc.files))
			if r.selfHosted != tc.selfHosted || r.public != tc.public {
				t.Fatalf("counted %d self-hosted / %d public, want %d / %d", r.selfHosted, r.public, tc.selfHosted, tc.public)
			}
			if len(r.failures) != len(tc.wantFailures) {
				t.Fatalf("failures %q, want %d", r.failures, len(tc.wantFailures))
			}
			for i, want := range tc.wantFailures {
				if !strings.Contains(r.failures[i], want) {
					t.Fatalf("failure %q does not name %q", r.failures[i], want)
				}
			}
		})
	}
}

// TestGuardProblemNamesTheMissingHalf pins the verdict function itself, since
// the real workflow files (once fixed) only ever exercise its happy path.
func TestGuardProblemNamesTheMissingHalf(t *testing.T) {
	cases := []struct {
		name string
		cond string
		want string // substring of the verdict; "" means accepted
	}{
		{"exact guard", forkGuard, ""},
		{"exact guard, surrounding whitespace", "  " + forkGuard + "\n", ""},
		{"absent", "", "NO `if:`"},
		{"only the same-repo half", sameRepoHalf, "lacks the `" + eventNameHalf + "`"},
		{"only the event_name half", eventNameHalf, "lacks the `" + sameRepoHalf + "`"},
		{"both halves, different spelling", sameRepoHalf + " || " + eventNameHalf, "not the exact guard"},
		{"both halves plus an extra clause", forkGuard + " || github.actor == 'dependabot[bot]'", "not the exact guard"},
		{"unrelated condition", "always()", "lacks the `" + eventNameHalf + "`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guardProblem(tc.cond)
			if tc.want == "" && got != "" {
				t.Fatalf("accepted guard was refused: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("verdict %q does not name %q", got, tc.want)
			}
		})
	}
}

// The adversarial fixtures: every runs-on shape that must count as
// self-hosted, and every gate shape that must be refused.
var adversarialRunsOn = []struct {
	runsOn     string
	selfHosted bool
}{
	{"example-arm64", true},
	{"[linux, example-arm64]", true},
	{"[ubuntu-latest, gpu]", true},
	{"ubuntu-lab", true},
	{"[]", true},
	{"ubuntu-latest", false},
	{"ubuntu-24.04-arm", false},
	{"ubuntu-latest-8-cores", false},
	{"windows-2025", false},
	{"windows-11-arm", false},
	{"macos-14-large", false},
	{"macos-latest-xlarge", false},
	{"[ubuntu-24.04, Ubuntu-22.04]", false},
}

var adversarialGates = []struct {
	cond     string
	accepted bool
}{
	{"vars.X == 'true' || vars.Y == 'z' && (" + forkGuard + ")", false},
	{"vars.X == 'true' && vars.Y != 'z' && (" + forkGuard + ")", false},
	{"vars.X == 'true' && (" + forkGuard + ") || (always())", false},
	{"vars.X == 'true' && always() && (" + forkGuard + ")", false},
	{"vars.X == 'true' && !vars.Y && (" + forkGuard + ")", false},
	{"vars.X || (" + forkGuard + ")", false},
	{"vars.X == 'true' && (" + forkGuard + ")", true},
	{"vars.X == 'true' && vars.Y == 'on' && (" + forkGuard + ")", true},
}

// TestAdversarialRunsOnAndGates pins the two holes this test and its Python
// port shared: a job on a machine label with no `self-hosted` and no `if:`
// was not counted as self-hosted, and a variable-gate prefix was skipped to
// the first `&& (` rather than read.
func TestAdversarialRunsOnAndGates(t *testing.T) {
	for _, tc := range adversarialRunsOn {
		t.Run("runs-on "+tc.runsOn, func(t *testing.T) {
			var wf workflow
			if err := yaml.Unmarshal([]byte("on: [pull_request]\njobs:\n  a:\n    runs-on: "+tc.runsOn+"\n"), &wf); err != nil {
				t.Fatal(err)
			}
			r := checkForkGuard(t, map[string]workflow{"w.yml": wf})
			want := 0
			if tc.selfHosted {
				want = 1
			}
			if r.selfHosted != want {
				t.Fatalf("counted %d self-hosted, want %d (failures %q)", r.selfHosted, want, r.failures)
			}
			if tc.selfHosted && (len(r.failures) != 1 || !strings.Contains(r.failures[0], "NO `if:`")) {
				t.Fatalf("a guardless self-hosted job was not refused: %q", r.failures)
			}
			if !tc.selfHosted && len(r.failures) != 0 {
				t.Fatalf("a hosted job was refused: %q", r.failures)
			}
		})
	}
	for _, tc := range adversarialGates {
		t.Run("if "+tc.cond, func(t *testing.T) {
			got := guardProblem(tc.cond)
			if tc.accepted && got != "" {
				t.Fatalf("an accepted gate was refused: %s", got)
			}
			if !tc.accepted && got == "" {
				t.Fatal("an adversarial gate was accepted")
			}
		})
	}
	// Gate cases whose verdict must name the missing part.
	for _, tc := range []struct{ cond, want string }{
		{"vars.EXAMPLE_CI_ENABLED == 'true' && (" + sameRepoHalf + ")", "lacks the"},
		{"vars.EXAMPLE_CI_ENABLED == 'true' && " + forkGuard, "variable gate"},
	} {
		if got := guardProblem(tc.cond); !strings.Contains(got, tc.want) {
			t.Errorf("%q: verdict %q does not name %q", tc.cond, got, tc.want)
		}
	}
}

// adversarialPublic is ADVERSARIAL_PUBLIC in
// tools/publication-guard/check_workflows_test.py, IDENTICAL inputs: change
// both or neither. guardOK is this test's verdict;
// publicOK is check_workflows.py's, which has no `if:` concept and judges the
// runner alone — so `gate && (a) || (b)` on the exact expression is refused
// here (the `||` escapes the gate: not public, self-hosted-capable, no fork
// guard) and accepted there (the runner is the hosted expression either way).
var adversarialPublic = []struct {
	name, cond, runsOn string
	guardOK, publicOK  bool
}{
	{"A1 a mention with ||, a string-literal runner", "vars.PUBLIC_CI_ENABLED == 'true' || github.event_name == 'pull_request'", "${{ 'example-arm64' }}", false, false},
	{"A2 a string-literal gate, a format() runner", `"'vars.PUBLIC_CI_ENABLED' != ''"`, "${{ format('{0}-arm64','example') }}", false, false},
	{"exact gate, a string-literal runner", "vars.PUBLIC_CI_ENABLED == 'true'", "${{ 'example-arm64' }}", false, false},
	{"exact gate, a non-hosted fallback", "vars.PUBLIC_CI_ENABLED == 'true'", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '["example-arm64"]') }}`, false, false},
	{"exact gate, an empty fallback", "vars.PUBLIC_CI_ENABLED == 'true'", "${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '[]') }}", false, false},
	{"exact gate, another variable", "vars.PUBLIC_CI_ENABLED == 'true'", `${{ fromJSON(vars.OTHER_RUNS_ON || '["ubuntu-24.04"]') }}`, false, false},
	{"exact gate, an || after fromJSON", "vars.PUBLIC_CI_ENABLED == 'true'", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '["ubuntu-24.04"]') || 'example-arm64' }}`, false, false},
	{"exact gate, the exact expression", "vars.PUBLIC_CI_ENABLED == 'true'", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '["ubuntu-24.04"]') }}`, true, true},
	{"exact gate, the per-architecture variable", "vars.PUBLIC_CI_ENABLED == 'true'", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON_ARM64 || '["ubuntu-24.04-arm"]') }}`, true, true},
	{"exact gate, the per-architecture variable, a non-hosted fallback", "vars.PUBLIC_CI_ENABLED == 'true'", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON_AMD64 || '["example-arm64"]') }}`, false, false},
	{"exact gate, a look-alike suffix", "vars.PUBLIC_CI_ENABLED == 'true'", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON_X || '["ubuntu-24.04"]') }}`, false, false},
	{"gate and another variable, a hosted literal", "vars.PUBLIC_CI_ENABLED == 'true' && vars.Y == 'x'", "ubuntu-24.04", true, true},
	{"exact gate, a hosted literal list", "vars.PUBLIC_CI_ENABLED == 'true'", "[ubuntu-24.04, ubuntu-24.04-arm]", true, true},
	{"the guard.yml shape", "vars.PUBLIC_CI_ENABLED == 'true' && (github.event_name == 'push' || github.event_name == 'merge_group')", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '["ubuntu-24.04"]') }}`, true, true},
	{"gate && (a) || (b)", "vars.PUBLIC_CI_ENABLED == 'true' && (github.event_name == 'push') || (github.event_name == 'pull_request')", `${{ fromJSON(vars.PUBLIC_CI_RUNS_ON || '["ubuntu-24.04"]') }}`, false, true},
}

// TestAdversarialPublicJobs pins two properties: a job whose `if:` merely
// mentions the gate variable is not a public job, and a public job cannot run
// on an arbitrary expression (`${{ 'example-arm64' }}`).
func TestAdversarialPublicJobs(t *testing.T) {
	for _, tc := range adversarialPublic {
		t.Run(tc.name, func(t *testing.T) {
			text := "on: [pull_request]\njobs:\n  a:\n    if: " + tc.cond + "\n    runs-on: " + tc.runsOn + "\n    steps: []\n"
			var wf workflow
			if err := yaml.Unmarshal([]byte(text), &wf); err != nil {
				t.Fatal(err)
			}
			r := checkForkGuard(t, map[string]workflow{"w.yml": wf})
			if got := len(r.failures) == 0; got != tc.guardOK {
				t.Fatalf("accepted=%v, want %v (failures %q)", got, tc.guardOK, r.failures)
			}
		})
	}
}
