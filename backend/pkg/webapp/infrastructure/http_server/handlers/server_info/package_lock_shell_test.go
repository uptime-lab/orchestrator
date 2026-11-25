package server_info

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// The device scripts are run here for real, a section at a time, under sh
// with stub programs on PATH: a dpkg that reports the lock held for as long
// as a test says, a dpkg-query that answers from what the stub dpkg did, and a
// sleep that returns at once. The real /proc and dpkg lock paths are pointed
// at a scratch directory, so nothing on this machine is read or written.

func serveInstall(t *testing.T) string {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.AdvertisementURL = "https://example.test"
	h := NewHandler(cfg, true, slog.Default())
	rec := httptest.NewRecorder()
	if err := h.GetInstallScript(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/install.sh", nil), rec)); err != nil {
		t.Fatalf("GetInstallScript: %v", err)
	}
	return rec.Body.String()
}

// section returns the script text from the line containing `from` up to (not
// including) the line containing `to`; an empty `to` means the end.
func section(t *testing.T, body, from, to string) string {
	t.Helper()
	i := strings.Index(body, from)
	if i < 0 {
		t.Fatalf("the script no longer contains %q", from)
	}
	i = strings.LastIndex(body[:i], "\n") + 1
	if to == "" {
		return body[i:]
	}
	j := strings.Index(body[i:], to)
	if j < 0 {
		t.Fatalf("the script no longer contains %q after %q", to, from)
	}
	j = strings.LastIndex(body[:i+j], "\n") + 1
	return body[i:j]
}

// lockFixture is a scratch /proc and a pair of dpkg lock files, with the
// lock-frontend file held by one process as /proc/locks would show it.
type lockFixture struct {
	dir, proc, frontend, dbLock string
}

func newLockFixture(t *testing.T, pid int, cmdline string) *lockFixture {
	t.Helper()
	dir := t.TempDir()
	f := &lockFixture{
		dir:      dir,
		proc:     filepath.Join(dir, "proc"),
		frontend: filepath.Join(dir, "lock-frontend"),
		dbLock:   filepath.Join(dir, "lock"),
	}
	for _, p := range []string{f.frontend, f.dbLock} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(f.proc, strconv.Itoa(pid)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.proc, strconv.Itoa(pid), "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	ino := inode(t, f.frontend)
	// A waiter ("->") on the same file must not be taken for the holder.
	locks := "1: POSIX  ADVISORY  WRITE " + strconv.Itoa(pid) + " 08:02:" + ino + " 0 EOF\n" +
		"1: -> POSIX  ADVISORY  WRITE 999 08:02:" + ino + " 0 EOF\n" +
		"2: FLOCK  ADVISORY  WRITE 77 08:02:1 0 EOF\n"
	if err := os.WriteFile(filepath.Join(f.proc, "locks"), []byte(locks), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func inode(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no inode numbers on this platform")
	}
	return strconv.FormatUint(uint64(st.Ino), 10)
}

// point rewrites the snippet's paths and clock to the fixture's. On a script
// that predates the snippet the replacements find nothing, which is the point:
// the same test then exercises the old code.
func (f *lockFixture) point(script string, max, poll int) string {
	script = strings.Replace(script, `PKG_LOCK_FILES="/var/lib/dpkg/lock-frontend /var/lib/dpkg/lock"`,
		`PKG_LOCK_FILES="`+f.frontend+` `+f.dbLock+`"`, 1)
	script = strings.Replace(script, "PKG_PROC=/proc", "PKG_PROC="+f.proc, 1)
	script = strings.Replace(script, "PKG_LOCK_MAX=2700", "PKG_LOCK_MAX="+strconv.Itoa(max), 1)
	script = strings.Replace(script, "PKG_LOCK_POLL=5", "PKG_LOCK_POLL="+strconv.Itoa(poll), 1)
	return script
}

// stubs writes the fake package tools. dpkg answers every install or removal
// with the lock error until it has been called lockedCalls times, then
// succeeds; dpkg-query reports the agent installed or not from a state file
// the stub dpkg maintains. sleep returns at once.
func stubs(t *testing.T, dir string, lockedCalls int, installedBefore bool) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "installed")
	if installedBefore {
		if err := os.WriteFile(state, []byte("yes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(dir, "dpkg-calls")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("dpkg", `n=$(cat "`+calls+`" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "`+calls+`"
if [ "$n" -le `+strconv.Itoa(lockedCalls)+` ]; then
  echo "dpkg: error: dpkg frontend lock was locked by another process with pid 4242" >&2
  exit 2
fi
case "$1" in
  -i) echo yes > "`+state+`" ;;
  --purge|--remove) rm -f "`+state+`" ;;
esac
exit 0
`)
	write("dpkg-query", `if [ -f "`+state+`" ]; then printf 'install ok installed'; else printf 'unknown ok not-installed'; fi
`)
	write("apt-get", `echo apt-get "$@" >> "`+filepath.Join(dir, "apt-calls")+`"; exit 100
`)
	write("sleep", "exit 0\n")
	write("systemctl", "exit 0\n")
	return bin
}

func runSh(t *testing.T, script, bin string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		t.Fatalf("sh: %v\n%s", err, out)
		return "", -1
	}
}

const unattendedUpgrade = "/usr/bin/python3\x00/usr/bin/unattended-upgrade\x00--download-only\x00"

// installStep is the package-install part of install.sh, run on its own.
func installStep(t *testing.T, f *lockFixture, max, poll int) string {
	body := f.point(serveInstall(t), max, poll)
	return "set -eu\nTMP_DEB=/nonexistent.deb\n" +
		section(t, body, `echo "Installing agent..."`, "# Record the signature")
}

// A fresh system's first automatic upgrade holds the package lock for far
// longer than five minutes. The install waits it out, says who it waits for
// and for how long, and installs once the lock is free.
func TestInstallWaitsOutALongPackageLockAndNamesTheHolder(t *testing.T) {
	f := newLockFixture(t, 4242, unattendedUpgrade)
	// Held for 25 polls of a minute each: 25 minutes.
	bin := stubs(t, f.dir, 25, false)
	out, code := runSh(t, installStep(t, f, 2700, 60), bin)
	if code != 0 {
		t.Fatalf("the install gave up on a 25-minute lock (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "Waiting for unattended-upgrade (pid 4242) to release the package lock") {
		t.Errorf("the wait does not name the lock holder:\n%s", out)
	}
	if !strings.Contains(out, "24m elapsed") || !strings.Contains(out, "first upgrade can take up to half an hour") {
		t.Errorf("the wait does not report its progress:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "apt-calls")); err == nil {
		t.Error("a lock wait fell through to apt-get, which waits on the same lock")
	}
}

// A lock that outlasts the bound ends the run non-zero, with the holder named
// and the fact that nothing was installed — never a success, and never a
// fall-through to apt-get, which would only wait on the same lock again.
func TestInstallFailsClearlyWhenTheLockOutlastsTheBound(t *testing.T) {
	f := newLockFixture(t, 4242, unattendedUpgrade)
	bin := stubs(t, f.dir, 1000, false)
	out, code := runSh(t, installStep(t, f, 300, 60), bin)
	if code != 7 {
		t.Fatalf("exit = %d, want 7 (not installed):\n%s", code, out)
	}
	for _, want := range []string{"the agent was NOT installed", "still held by unattended-upgrade (pid 4242)", "Run the same command again"} {
		if !strings.Contains(out, want) {
			t.Errorf("the failure lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "apt-calls")); err == nil {
		t.Error("a lock timeout fell through to apt-get")
	}
}

// dpkg answering 0 is not the proof: the package manager must list the agent
// as installed, or the run fails.
func TestInstallFailsWhenThePackageIsNotInstalledAfterwards(t *testing.T) {
	f := newLockFixture(t, 4242, unattendedUpgrade)
	bin := stubs(t, f.dir, 0, false)
	// A dpkg that answers 0 and installs nothing.
	if err := os.WriteFile(filepath.Join(bin, "dpkg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := runSh(t, installStep(t, f, 2700, 60), bin)
	if code != 7 || !strings.Contains(out, "reports orchestrator-agent as not installed") {
		t.Fatalf("exit = %d, want 7 with the reason:\n%s", code, out)
	}
}

// connectStep is the part of install.sh that waits for the first connection.
func connectStep(t *testing.T, journal string, active bool) (string, int) {
	t.Helper()
	body := serveInstall(t)
	step := section(t, body, "# Wait for the agent to actually reach the platform", "")
	script := "set -eu\nPLATFORM_URL=https://example.test\nSTART_TS=now\n" + step
	bin := t.TempDir()
	write := func(name, b string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if active {
		write("systemctl", "exit 0\n")
	} else {
		write("systemctl", "exit 3\n")
	}
	write("journalctl", "cat <<'EOF'\n"+journal+"\nEOF\n")
	write("sleep", "exit 0\n")
	return runSh(t, script, bin)
}

// The page promises the script waits for the first connection before it
// reports success. An agent that never connects is not a success.
func TestInstallDoesNotReportSuccessWithoutAConnection(t *testing.T) {
	out, code := connectStep(t, "agent started", true)
	if code != 8 {
		t.Fatalf("exit = %d, want 8 (running, not connected):\n%s", code, out)
	}
	for _, want := range []string{"has not reached the platform", "keeps trying", "journalctl -u orchestrator-agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("the message lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "OK:") {
		t.Errorf("an unconnected agent was reported as OK:\n%s", out)
	}
}

func TestInstallConnectionOutcomes(t *testing.T) {
	if out, code := connectStep(t, "gRPC connection established endpoint=x", true); code != 0 || !strings.Contains(out, "OK:") {
		t.Errorf("a connected agent: exit %d\n%s", code, out)
	}
	if out, code := connectStep(t, "gRPC connection established\nthe platform refused this device: the install is at its device cap.", true); code != 9 {
		t.Errorf("a device refused at the cap: exit %d, want 9\n%s", code, out)
	}
	if out, code := connectStep(t, "", false); code != 5 || !strings.Contains(out, "service is not running") {
		t.Errorf("a stopped service: exit %d, want 5\n%s", code, out)
	}
}

// uninstallTail runs uninstall.sh from the agent stop to its end, with the
// commands that remove files replaced by no-ops and /etc/environment pointed
// at a scratch path: what is under test is what the run reports.
func uninstallTail(t *testing.T, f *lockFixture, max, poll int) string {
	body := f.point(serveUninstall(t, true), max, poll)
	tail := section(t, body, `echo "Stopping the agent..."`, "")
	tail = strings.ReplaceAll(tail, "/etc/environment", filepath.Join(f.dir, "environment"))
	return "set -eu\nDRY=\nPURGE_ACCESS=\nFORCE=\nrun() { :; }\ngone() { :; }\n" + tail
}

// A held lock used to make the purge fail silently and the script print
// "Done. The agent is removed" with the package still installed.
func TestUninstallReportsAPackageItCouldNotRemove(t *testing.T) {
	f := newLockFixture(t, 4242, unattendedUpgrade)
	bin := stubs(t, f.dir, 1000, true)
	out, code := runSh(t, uninstallTail(t, f, 300, 60), bin)
	if code != 8 {
		t.Fatalf("exit = %d, want 8 (package still installed):\n%s", code, out)
	}
	if strings.Contains(out, "Done. The agent is removed") {
		t.Errorf("the run claims a removal that did not happen:\n%s", out)
	}
	for _, want := range []string{"NOT DONE", "orchestrator-agent", "still held by unattended-upgrade (pid 4242)", "Run this script again"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
}

// The same lock released within the bound: the purge waits, succeeds, and the
// run says done.
func TestUninstallWaitsForThePackageLock(t *testing.T) {
	f := newLockFixture(t, 4242, unattendedUpgrade)
	bin := stubs(t, f.dir, 10, true)
	out, code := runSh(t, uninstallTail(t, f, 2700, 60), bin)
	if code != 0 || !strings.Contains(out, "Done. The agent is removed") {
		t.Fatalf("exit = %d, want a completed removal:\n%s", code, out)
	}
	if !strings.Contains(out, "Waiting for unattended-upgrade (pid 4242)") {
		t.Errorf("the wait does not name the holder:\n%s", out)
	}
}

// The holder is named the way the agent names it: the script an interpreter
// runs, the module after -m, never any inline code.
func TestPackageLockHolderNames(t *testing.T) {
	for cmdline, want := range map[string]string{
		unattendedUpgrade:                         "unattended-upgrade (pid 4242)",
		"/usr/bin/apt-get\x00install\x00-y\x00":   "apt-get (pid 4242)",
		"python3\x00-c\x00print('secret')\x00":    "python3 (pid 4242)",
		"/usr/bin/python3\x00-m\x00aptdaemon\x00": "aptdaemon (pid 4242)",
		"": "process 4242",
	} {
		f := newLockFixture(t, 4242, cmdline)
		script := f.point(packageLockShell, 2700, 5) + "\npkg_lock_holder\n"
		out, code := runSh(t, script, os.Getenv("PATH"))
		if code != 0 || strings.TrimSpace(out) != want {
			t.Errorf("cmdline %q: got %q (exit %d), want %q", cmdline, strings.TrimSpace(out), code, want)
		}
	}
}
