package server_info

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

func serveUninstall(t *testing.T, agentCDN bool) string {
	t.Helper()
	cfg := &config.Config{}
	h := NewHandler(cfg, agentCDN, slog.Default())
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/uninstall.sh", nil)
	rec := httptest.NewRecorder()
	if err := h.GetUninstallScript(e.NewContext(req, rec)); err != nil {
		t.Fatalf("GetUninstallScript: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func TestUninstallScriptIsValidPOSIXShell(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(serveUninstall(t, true))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall.sh failed `sh -n`: %v\n%s", err, out)
	}
}

// The ordering IS the feature. watchdog.service reboots the machine when the
// agent stops touching its liveness file, and stopping the agent is what this
// script does next — so the disarm has to come first. A reordering that looks
// harmless in review is exactly the failure this asserts against.
func TestUninstallScriptDisarmsTheWatchdogBeforeStoppingTheAgent(t *testing.T) {
	body := serveUninstall(t, true)

	disarm := strings.Index(body, "disable --now watchdog.service")
	stopAgent := strings.Index(body, "disable --now orchestrator-agent.service")
	if disarm < 0 || stopAgent < 0 {
		t.Fatalf("expected both a watchdog disarm and an agent stop; disarm=%d stop=%d", disarm, stopAgent)
	}
	if disarm > stopAgent {
		t.Fatal("the agent is stopped before the watchdog is disarmed — an armed watchdog will reboot the device")
	}
}

// The adoption file is the device's credential for the account. An uninstall
// that leaves it behind has not offboarded anything.
func TestUninstallScriptRemovesTheIdentity(t *testing.T) {
	body := serveUninstall(t, true)
	for _, want := range []string{
		"/etc/orchestrator",
		"/var/lib/orchestrator-agent",
		"/run/orchestrator-agent",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("uninstall.sh never removes %s", want)
		}
	}
}

// Deleting an OS user the platform created is the one irreversible mistake
// available here: it is usually the operator's own login.
func TestUninstallScriptNeverDeletesOSUsers(t *testing.T) {
	body := serveUninstall(t, true)
	for _, forbidden := range []string{"userdel", "deluser"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("uninstall.sh calls %q — it must remove grants, never accounts", forbidden)
		}
	}
	// groupdel of our own authority marker is fine and is not a login.
	if !strings.Contains(body, "groupdel orchestrator-managed") {
		t.Error("uninstall.sh should drop the orchestrator-managed group under --purge-access")
	}
}

// --purge-access can remove the only way into the box. It must refuse rather
// than ask for forgiveness.
func TestUninstallScriptRefusesToRemoveTheLastWayIn(t *testing.T) {
	body := serveUninstall(t, true)
	if !strings.Contains(body, "REFUSED:") {
		t.Fatal("uninstall.sh has no refusal path for --purge-access")
	}
	if !strings.Contains(body, "--force") {
		t.Error("the refusal must name the override, or an operator with console access is stuck")
	}
	// The refusal has to say what already happened, because the agent is gone by
	// the time this runs and "it failed" would read as "nothing was removed".
	if !strings.Contains(body, "already been removed") {
		t.Error("the refusal must say the agent removal already succeeded")
	}
}

// Unlike install.sh, this must be served even where the agent CDN is absent:
// an install with no CDN can still have agents in the field, and refusing here
// would strand exactly the people who need it.
func TestUninstallScriptIsServedWithoutTheAgentCDN(t *testing.T) {
	if body := serveUninstall(t, false); !strings.Contains(body, "Orchestrator agent uninstaller") {
		t.Fatal("uninstall.sh must be available on an install with no agent CDN")
	}
}

// It carries no platform state, so nothing needs substituting into it — and a
// script with an unreplaced placeholder would be piped into a root shell.
func TestUninstallScriptHasNoPlaceholders(t *testing.T) {
	if body := serveUninstall(t, true); strings.Contains(body, "__") {
		t.Error("uninstall.sh contains a placeholder-looking token")
	}
}

// The watcher is the other form of the agent and holds a node credential of
// its own; an offboarding that leaves it running has offboarded nothing.
func TestUninstallScriptRemovesTheWatcher(t *testing.T) {
	body := serveUninstall(t, true)
	for _, want := range []string{
		"disable --now orchestrator-watcher.service",
		"remove_package orchestrator-watcher",
		"/etc/orchestrator-watcher",
		"/usr/bin/orchestrator-watcher",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("uninstall.sh never does %q", want)
		}
	}
}

// runUninstallDryRun runs the served script with --dry-run as a pretend root,
// with stubs for the system tools, so it changes nothing and reads only what
// the stubs answer. bootloaderConfig is what rpi-eeprom-config prints.
func runUninstallDryRun(t *testing.T, bootloaderConfig string) string {
	t.Helper()
	bin := t.TempDir()
	stub := func(name, script string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("id", "echo 0\n")
	stub("systemctl", "exit 0\n")
	stub("rpi-eeprom-config", "cat <<'EOF'\n"+bootloaderConfig+"\nEOF\n")
	cmd := exec.Command("sh", "-s", "--", "--dry-run")
	cmd.Stdin = strings.NewReader(serveUninstall(t, true))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("uninstall.sh --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Dry run complete") {
		t.Fatalf("the stubbed run did not reach its end:\n%s", out)
	}
	return string(out)
}

// The bootloader of a network-booted device keeps its own copy of the node
// credential, which the script cannot remove. It must say so, and say what
// revokes it, rather than declare the identity gone.
func TestUninstallScriptReportsTheBootloaderCredential(t *testing.T) {
	if strings.Contains(serveUninstall(t, true), "no longer holds an adoption identity") {
		t.Error("uninstall.sh still claims the device holds no identity, which its bootloader may contradict")
	}
	out := runUninstallDryRun(t, "BOOT_UART=1\nBOOT_ORDER=0xf241\nUPTIME_TOKEN=eyJub2RlX2lkIjoieCJ9")
	for _, want := range []string{"bootloader (EEPROM) still holds the node credential", "delete the node on the platform", "forget the deleted node"} {
		if !strings.Contains(out, want) {
			t.Errorf("with UPTIME_TOKEN in the bootloader, the output lacks %q:\n%s", want, out)
		}
	}
}

func TestUninstallScriptStaysQuietWithoutABootloaderCredential(t *testing.T) {
	out := runUninstallDryRun(t, "BOOT_UART=1\nBOOT_ORDER=0xf41")
	if strings.Contains(out, "still holds the node credential") {
		t.Errorf("a bootloader without UPTIME_TOKEN was reported as holding a credential:\n%s", out)
	}
	if !strings.Contains(out, "delete the node there to finish") {
		t.Errorf("the output no longer tells the operator to delete the node:\n%s", out)
	}
}

// Environment secrets an older agent wrote into /etc/environment are a
// credential like the adoption file: an offboarding that leaves them leaves
// them readable by every login session.
func TestUninstallScriptRemovesTheLegacyEnvironmentBlock(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	body := serveUninstall(t, true)
	i := strings.Index(body, "ENV_BEGIN=")
	j := strings.Index(body, "run systemctl daemon-reload")
	if i < 0 || j < i {
		t.Fatalf("the script has no environment-block step before the reload")
	}
	dir := t.TempDir()
	env := filepath.Join(dir, "environment")
	content := "PATH=/usr/bin\n# >>> orchestrator:environment >>>\nAPI_KEY='secret-value'\n# <<< orchestrator:environment <<<\nLANG=C\n"
	if err := os.WriteFile(env, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	step := strings.ReplaceAll(body[i:j], "/etc/environment", env)
	out, err := exec.Command(sh, "-c", "DRY=\n"+step).CombinedOutput()
	if err != nil {
		t.Fatalf("step failed: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(env)
	if strings.Contains(string(got), "secret-value") || strings.Contains(string(got), "orchestrator:environment") {
		t.Fatalf("the block survived:\n%s", got)
	}
	if string(got) != "PATH=/usr/bin\nLANG=C\n" {
		t.Fatalf("lines outside the block changed:\n%s", got)
	}

	// One marker only: nothing is removed.
	half := "A=1\n# >>> orchestrator:environment >>>\nB=2\n"
	if err := os.WriteFile(env, []byte(half), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-c", "DRY=\n"+step).CombinedOutput(); err != nil {
		t.Fatalf("step failed: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(env); string(got) != half {
		t.Fatalf("a half-marked file was edited:\n%s", got)
	}

	// Markers present but not one begin line followed by one end line: the
	// file is left exactly as it is, and the run says so. Out of order, a
	// skip that starts at the begin marker would never end and would cut
	// every later line.
	for name, malformed := range map[string]string{
		"end before begin": "# <<< orchestrator:environment <<<\nOPERATOR=1\n# >>> orchestrator:environment >>>\nX=1\nLANG=C\n",
		"two blocks":       "# >>> orchestrator:environment >>>\nX=1\n# <<< orchestrator:environment <<<\nKEEP=1\n# >>> orchestrator:environment >>>\nY=2\n# <<< orchestrator:environment <<<\nLANG=C\n",
		"end only":         "A=1\n# <<< orchestrator:environment <<<\nB=2\n",
	} {
		if err := os.WriteFile(env, []byte(malformed), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(sh, "-c", "set -eu\nDRY=\n"+step).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: step failed: %v\n%s", name, err, out)
		}
		if got, _ := os.ReadFile(env); string(got) != malformed {
			t.Errorf("%s: the file was edited:\n%s", name, got)
		}
		if !strings.Contains(string(out), "unchanged: its orchestrator markers are not") {
			t.Errorf("%s: the run does not say the file was left alone:\n%s", name, out)
		}
	}
}

// A dry run changes nothing, so when --purge-access is refused there it must
// not report the agent as already removed.
func TestUninstallScriptDryRunRefusalSaysNothingWasRemoved(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\necho 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No account has keys of its own: point the survivor scan at an empty
	// account list instead of this machine's.
	const scan = "PASSWD_FILE=/etc/passwd"
	body := serveUninstall(t, true)
	if !strings.Contains(body, scan) {
		t.Fatalf("the script no longer scans %q", scan)
	}
	empty := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, scan, "PASSWD_FILE="+empty, 1)

	cmd := exec.Command("sh", "-s", "--", "--dry-run", "--purge-access")
	cmd.Stdin = strings.NewReader(body)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 7 {
		t.Fatalf("want the refusal (exit 7), got %v:\n%s", err, out)
	}
	if strings.Contains(string(out), "already been removed") {
		t.Errorf("the dry run claims the agent was removed:\n%s", out)
	}
	if !strings.Contains(string(out), "nothing was removed") {
		t.Errorf("the dry-run refusal does not say nothing was removed:\n%s", out)
	}
}
