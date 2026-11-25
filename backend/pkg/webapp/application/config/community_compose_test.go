package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The community compose is the configuration a self-hosted install starts
// from, so the properties its security acceptances rest on are pinned HERE,
// against the file itself, rather than restated in a document that cannot
// notice the file changing (docs/SECURITY-ACCEPTANCES.md cites this test).
//
// The file sits at the repository root, beside the backend module, in the
// working repository and in the public tree alike. It is found by walking up
// to the module root (go.mod) and looking beside it — never by a fixed
// number of `..`. A checkout that holds the backend module ALONE (the Docker
// test context of `make backend/test-docker`, which copies only backend/)
// has no such file: there these tests SKIP, naming the file, and under
// ORCHESTRATOR_CONTRACT_STRICT=1 they FAIL — the CI jobs that check out the
// whole repository run them strictly, so the pin is enforced somewhere.
const communityComposeName = "docker-compose.community.yml"

// communityComposePath returns the community compose beside the backend
// module root, or "" when this checkout carries no such file.
func communityComposePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s: this test's view of the module is stale", dir)
		}
		dir = parent
	}
	candidate := filepath.Join(filepath.Dir(dir), communityComposeName)
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

func readCommunityCompose(t *testing.T) string {
	t.Helper()
	path := communityComposePath(t)
	if path == "" {
		if os.Getenv("ORCHESTRATOR_CONTRACT_STRICT") == "1" {
			t.Fatalf("%s is not beside the backend module and ORCHESTRATOR_CONTRACT_STRICT=1: the community compose pins cannot be checked", communityComposeName)
		}
		t.Skipf("%s is not beside the backend module (a backend-only checkout, such as the Docker test context); set ORCHESTRATOR_CONTRACT_STRICT=1 to make this a failure", communityComposeName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the community compose: %v", err)
	}
	return string(data)
}

// backendEnvironment parses the backend service's `environment` list the way
// compose reads it: YAML comments are not entries, every `- NAME=value` item
// counts, and a later assignment of the same name replaces an earlier one.
// A duplicate is refused outright, because a second line silently overriding
// the pinned one is exactly what a text search for the first match misses.
func backendEnvironment(compose string) (map[string]string, error) {
	var doc struct {
		Services map[string]struct {
			Environment yaml.Node `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return nil, fmt.Errorf("parse the compose file: %w", err)
	}
	backend, ok := doc.Services["backend"]
	if !ok {
		return nil, fmt.Errorf("the compose file has no backend service")
	}
	var entries []string
	if err := backend.Environment.Decode(&entries); err != nil {
		return nil, fmt.Errorf("the backend environment must be a list of NAME=value entries: %w", err)
	}
	env := map[string]string{}
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		name = strings.TrimSpace(name)
		if _, dup := env[name]; dup {
			return nil, fmt.Errorf("%s is assigned more than once in the backend environment; the last assignment wins in compose", name)
		}
		env[name] = value
	}
	return env, nil
}

// backendEnvDefault returns what NAME resolves to when the operator's .env
// does not set it: the default of a `${NAME:-default}` substitution, or a
// literal value as written. Any other substitution is refused, because its
// value is not knowable from the file.
func backendEnvDefault(compose, name string) (string, error) {
	env, err := backendEnvironment(compose)
	if err != nil {
		return "", err
	}
	value, ok := env[name]
	if !ok {
		return "", fmt.Errorf("the backend environment does not set %s", name)
	}
	return substitutionDefault(name, value)
}

// substitutionDefault resolves a compose value that is either
// `${NAME:-default}` or a literal; anything else is unknowable from the file.
func substitutionDefault(name, value string) (string, error) {
	m := regexp.MustCompile(`^\$\{` + regexp.QuoteMeta(name) + `:-([^${}]*)\}$`).FindStringSubmatch(value)
	if m != nil {
		return m[1], nil
	}
	if strings.ContainsAny(value, "${}") {
		return "", fmt.Errorf("%s=%s: not a ${%s:-default} substitution or a literal", name, value, name)
	}
	return value, nil
}

func composeDefaultInt(t *testing.T, compose, name string) int {
	t.Helper()
	raw, err := backendEnvDefault(compose, name)
	if err != nil {
		t.Fatalf("the community compose's %s: %v", name, err)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s default %q is not an integer", name, raw)
	}
	return n
}

// backendMemoryLimit returns the backend service's container memory limit as
// the compose file writes it, read from the YAML: `deploy.resources.limits.
// memory` or the legacy `mem_limit`. Comments are not values, another
// service's limit is not the backend's, and a limit stated in both spellings
// is refused — compose reconciles the two, so a check reading one of them may
// be reading the one that loses. A duplicate key is refused by the parser.
func backendMemoryLimit(compose string) (string, error) {
	var doc struct {
		Services map[string]struct {
			MemLimit *string `yaml:"mem_limit"`
			Deploy   struct {
				Resources struct {
					Limits struct {
						Memory *string `yaml:"memory"`
					} `yaml:"limits"`
				} `yaml:"resources"`
			} `yaml:"deploy"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return "", fmt.Errorf("parse the compose file: %w", err)
	}
	backend, ok := doc.Services["backend"]
	if !ok {
		return "", fmt.Errorf("the compose file has no backend service")
	}
	deploy := backend.Deploy.Resources.Limits.Memory
	switch {
	case deploy != nil && backend.MemLimit != nil:
		return "", fmt.Errorf("the backend states its memory limit twice (deploy.resources.limits.memory and mem_limit)")
	case deploy != nil:
		return *deploy, nil
	case backend.MemLimit != nil:
		return *backend.MemLimit, nil
	}
	return "", fmt.Errorf("the backend service has no memory limit")
}

// backendMemoryLimitMB resolves the backend's memory limit — a
// `${NAME:-default}` substitution of the named variable, or a literal — in
// MiB, from a "1G" or "768M" form.
func backendMemoryLimitMB(compose, name string) (int, error) {
	value, err := backendMemoryLimit(compose)
	if err != nil {
		return 0, err
	}
	raw, err := substitutionDefault(name, value)
	if err != nil {
		return 0, err
	}
	raw = strings.ToUpper(strings.TrimSpace(raw))
	mult := 1
	switch {
	case strings.HasSuffix(raw, "G"):
		mult, raw = 1024, strings.TrimSuffix(raw, "G")
	case strings.HasSuffix(raw, "M"):
		raw = strings.TrimSuffix(raw, "M")
	default:
		return 0, fmt.Errorf("%s default %q: expected a G or M suffix", name, raw)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s default %q is not a number", name, raw)
	}
	return n * mult, nil
}

// composeMemoryMB reads the backend's container memory limit default in MiB.
func composeMemoryMB(t *testing.T, compose, name string) int {
	t.Helper()
	n, err := backendMemoryLimitMB(compose, name)
	if err != nil {
		t.Fatalf("the community compose's backend memory limit: %v", err)
	}
	return n
}

// The per-serial HTTP boot path is unauthenticated by construction, and its
// acceptance holds only while the boot-loop failsafe is OFF on an install
// whose boot host strangers can reach (docs/HTTPBOOT-AUTH-RESEARCH.md §2.1).
// The community compose describes exactly that install — a public DOMAIN,
// whose reverse proxy serves the boot path on port 80 — so its default must keep the
// failsafe off and the endpoint fail-closed. A LAN install arms it on
// purpose through .env; the default is what a stranger's install gets.
func TestCommunityComposeKeepsTheBootFailsafeOff(t *testing.T) {
	compose := readCommunityCompose(t)

	threshold := composeDefaultInt(t, compose, "PROVISIONING_BOOT_FAILSAFE_THRESHOLD")
	if threshold != 0 {
		t.Errorf("PROVISIONING_BOOT_FAILSAFE_THRESHOLD defaults to %d in the community compose; an internet-facing install must run it at 0", threshold)
	}
	failOpen, err := backendEnvDefault(compose, "PROVISIONING_HTTP_BOOT_FAIL_OPEN")
	if err != nil || failOpen != "false" {
		t.Errorf("PROVISIONING_HTTP_BOOT_FAIL_OPEN defaults to %q in the community compose, want \"false\" (fail-closed on an unknown serial)", failOpen)
	}

	// The same default, read through the code that warns about it: an
	// install started from this file must hear no failsafe advisory.
	cfg := productionReadyConfig()
	cfg.Provisioning.BootFailsafeThreshold = threshold
	for _, a := range cfg.Advisories() {
		if strings.Contains(a, "PROVISIONING_BOOT_FAILSAFE_THRESHOLD") {
			t.Errorf("the community default produces the failsafe advisory: %s", a)
		}
	}
	// And the built-in default agrees, so a hand-written environment that
	// omits the variable is not armed either.
	if got := getDefaultConfig().Provisioning.BootFailsafeThreshold; got != 0 {
		t.Errorf("getDefaultConfig() arms the failsafe at %d; the safe default is 0", got)
	}
}

// The community release ships no admin console, so its compose declares that
// nothing reads the instance anomalies: the start log must not warn its
// operator about a component the install does not have.
func TestCommunityComposeDeclaresNoAnomalyReader(t *testing.T) {
	compose := readCommunityCompose(t)
	reader, err := backendEnvDefault(compose, "INSTANCE_MONITORING_READER")
	if err != nil {
		t.Fatalf("the community compose's INSTANCE_MONITORING_READER: %v", err)
	}
	cfg := getDefaultConfig()
	cfg.Monitoring.AnomalyReader = reader
	if !cfg.AnomalyReaderDeclaredAbsent() {
		t.Errorf("INSTANCE_MONITORING_READER defaults to %q in the community compose; want %q, or every start warns about an admin console it does not ship", reader, AnomalyReaderNone)
	}
}
