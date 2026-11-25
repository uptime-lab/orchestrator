// Package repoguard holds cross-tree guards: tests that live in the backend
// module because it is the module CI already builds and runs Go tests for, but
// that read files OUTSIDE the module — today the GitHub Actions workflow files
// under .github/workflows.
//
// The package has no production code and nothing imports it. Each guard is a
// test that pins a property the repository relies on and that no compiler,
// linter or workflow run reports when it breaks, because the thing being
// guarded is the workflow configuration itself.
//
// Two rules every guard here follows, taken from the two older cross-tree
// guards (authz.TestFrontendAnyScopeReadsMatch and
// http_server.TestEveryPostmanRequestNamesARealRoute):
//
//   - It SKIPS, with a message naming the CI step that enforces it, when the
//     tree it reads is structurally absent — the docker module tests build
//     from backend/ alone and cannot see .github/.
//   - It belongs in the "Cross-tree guards" step of the backend job in
//     .github/workflows/ci.yml, where a SKIP in the full checkout is a
//     FAILURE. A guard that only ever skips is a dead guard.
package repoguard
