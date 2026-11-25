package server_info

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// The outcomes install.sh and uninstall.sh report, run under sh with stub
// programs on PATH. Nothing on this machine is read or written: every system
// path a step touches is pointed at a scratch directory first.

// agentLine is a journal line as the agent's text logger writes it.
func agentLine(level, msg, attrs string) string {
	return "Jan 01 00:00:00 host orchestrator-agent[1]: time=2026-01-01T00:00:00Z level=" + level +
		" msg=\"" + msg + "\" " + attrs
}

const establishedLine = `Jan 01 00:00:00 host orchestrator-agent[1]: time=2026-01-01T00:00:00Z level=INFO msg="gRPC connection established" endpoint=example.test:50051`

func receiveFailure(code, desc string) string {
	return agentLine("ERROR", "failed to receive message", `error="rpc error: code = `+code+` desc = `+desc+`"`)
}

// connectSequence runs the connection wait against a journal that grows: the
// n-th read of the journal returns every line whose index is below
// reveal[n] (the last value repeats). It models a refusal the server sends
// only after holding the stream open for a while.
func connectSequence(t *testing.T, lines []string, reveal []int) (string, int) {
	t.Helper()
	body := serveInstall(t)
	step := section(t, body, "# Wait for the agent to actually reach the platform", "")
	script := "set -eu\nPLATFORM_URL=https://example.test\nSTART_TS=now\n" + step
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, l := range lines {
		if err := os.WriteFile(filepath.Join(dir, "line"+strconv.Itoa(i)), []byte(l+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var revealList []string
	for _, r := range reveal {
		revealList = append(revealList, strconv.Itoa(r))
	}
	counter := filepath.Join(dir, "reads")
	journal := `n=$(cat "` + counter + `" 2>/dev/null || echo 0); echo $((n + 1)) > "` + counter + `"
i=0; show=0
for r in ` + strings.Join(revealList, " ") + `; do show=$r; [ "$i" -ge "$n" ] && break; i=$((i + 1)); done
j=0
while [ "$j" -lt "$show" ]; do cat "` + dir + `/line$j"; j=$((j + 1)); done
`
	write := func(name, b string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("journalctl", journal)
	write("systemctl", "exit 0\n")
	write("sleep", "exit 0\n")
	return runSh(t, script, bin)
}

// A refusal is not a connection. Each refusal the server answers a first
// connection with ends the run with its own status and the server's reason,
// never with the success line.
func TestInstallReportsEachRefusalWithItsOwnStatus(t *testing.T) {
	cases := []struct {
		code, desc string
		want       int
		says       string
	}{
		{"Unauthenticated", "connection failed", 11, "did not accept this device's credential"},
		{"PermissionDenied", "device cannot be adopted into this account", 12, "another account"},
		{"InvalidArgument", "a unique hardware serial is required for master token adoption (unsupported device)", 13, "unique hardware serial"},
	}
	for _, c := range cases {
		out, code := connectStep(t, establishedLine+"\n"+receiveFailure(c.code, c.desc), true)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d\n%s", c.code, code, c.want, out)
		}
		if strings.Contains(out, "OK:") {
			t.Errorf("%s: a refused device was reported as connected:\n%s", c.code, out)
		}
		if !strings.Contains(out, c.desc) || !strings.Contains(out, c.says) {
			t.Errorf("%s: the message lacks the server's reason %q or %q:\n%s", c.code, c.desc, c.says, out)
		}
	}
}

// The server holds a refused stream open for a while before it answers, so a
// refusal arrives well after the agent logs its connection. Success is not
// reported while that answer can still come.
func TestInstallWaitsForALateRefusal(t *testing.T) {
	lines := []string{establishedLine, receiveFailure("PermissionDenied", "device cannot be adopted into this account")}
	// The refusal shows up on the eighth read of the journal, 21 s after the
	// connection was logged.
	out, code := connectSequence(t, lines, []int{1, 1, 1, 1, 1, 1, 1, 2})
	if code != 12 || strings.Contains(out, "OK:") {
		t.Fatalf("exit %d, want 12 for a refusal that came after the connection line:\n%s", code, out)
	}
}

// A frame from the server is proof of acceptance: no need to wait out the
// refusal window.
func TestInstallTakesACommandAsAcceptance(t *testing.T) {
	lines := []string{establishedLine, agentLine("INFO", "command received", "command_id=abc")}
	out, code := connectSequence(t, lines, []int{2})
	if code != 0 || !strings.Contains(out, "OK:") {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

// "The device limit is reached" and "the device limit could not be checked"
// are different answers with different remedies: the first needs an operator
// to free a slot, the second clears on its own.
func TestInstallTellsAnUncheckedDeviceLimitFromAReachedOne(t *testing.T) {
	journal := establishedLine + "\n" + receiveFailure("ResourceExhausted", "device cap could not be checked; retry shortly")
	out, code := connectStep(t, journal, true)
	if code != 14 {
		t.Fatalf("exit %d, want 14 (device limit not checked):\n%s", code, out)
	}
	if strings.Contains(out, "reached its device limit") || !strings.Contains(out, "could not check its device limit") {
		t.Errorf("the message does not say the limit could not be checked:\n%s", out)
	}

	// The same refusal followed by a connection that holds is a success: the
	// agent asked again and was admitted.
	lines := []string{establishedLine, receiveFailure("ResourceExhausted", "device cap could not be checked; retry shortly"), establishedLine}
	out, code = connectSequence(t, lines, []int{2, 2, 3})
	if code != 0 {
		t.Fatalf("a retry admitted after an unchecked limit: exit %d, want 0:\n%s", code, out)
	}

	capWarn := agentLine("WARN", "the platform refused this device: the install is at its device cap. The agent keeps running and asks again periodically; it is admitted once an operator removes a device or applies an entitlement", "retry_in=1h0m0s")
	if out, code := connectStep(t, establishedLine+"\n"+capWarn, true); code != 9 {
		t.Errorf("a device refused at the cap: exit %d, want 9\n%s", code, out)
	}
}

// A refusal at the ACCOUNT's limit is its own status and its own remedy,
// whether the agent holds on it (its WARN) or predates it and only logs the
// receive failure.
func TestInstallReportsTheAccountLimitApart(t *testing.T) {
	warn := agentLine("WARN", "the platform refused this device: its account is at its device limit. The agent keeps running and asks again periodically; it is admitted once a device is removed or the limit is raised", "retry_in=15m0s")
	again := agentLine("INFO", "still refused at the device cap; asking again later", "refusals=2")
	for name, journal := range map[string]string{
		"held":         establishedLine + "\n" + warn,
		"held, repeat": establishedLine + "\n" + warn + "\n" + again,
		"older agent":  establishedLine + "\n" + receiveFailure("ResourceExhausted", "account device limit reached"),
	} {
		out, code := connectStep(t, journal, true)
		if code != 10 || !strings.Contains(out, "account has reached its device limit") || strings.Contains(out, "OK:") {
			t.Errorf("%s: exit %d, want 10 naming the account's limit:\n%s", name, code, out)
		}
	}
}

// fullInstall is a whole install.sh run on a stubbed device: arm64, root,
// curl answering the signature, the package and the code exchange, dpkg
// holding the lock for lockedCalls calls. It returns the output, the exit
// status and the log of curl calls in order.
type installRun struct {
	out, calls string
	code       int
	etc        string
}

func fullInstall(t *testing.T, token, arch string, lockedCalls, lockMax int, journal string) installRun {
	t.Helper()
	return fullInstallWith(t, "--token '"+token+"'", "", arch, lockedCalls, lockMax, journal)
}

// fullInstallWith is fullInstall with the script's arguments as given and,
// when etc is not empty, that directory standing in for /etc/orchestrator (so
// a second run sees what the first left).
func fullInstallWith(t *testing.T, args, etc, arch string, lockedCalls, lockMax int, journal string) installRun {
	t.Helper()
	f := newLockFixture(t, 4242, unattendedUpgrade)
	bin := stubs(t, f.dir, lockedCalls, false)
	if etc == "" {
		etc = filepath.Join(f.dir, "etc-orchestrator")
	}
	calls := filepath.Join(f.dir, "curl-calls")
	blob := base64.StdEncoding.EncodeToString([]byte(`{"platform_url":"https://example.test","node_id":"n"}`))
	write := func(name, b string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("curl", `echo "$*" >> "`+calls+`"
case "$*" in *adopt/exchange*) printf '%s' '`+blob+`'; exit 0 ;; esac
out=""
while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
[ -n "$out" ] && echo payload > "$out"
exit 0
`)
	write("uname", "echo "+arch+"\n")
	write("id", "echo 0\n")
	write("openssl", "exit 0\n")
	write("dpkg-deb", "exit 0\n")
	write("journalctl", "cat <<'EOF'\n"+journal+"\nEOF\n")
	body := f.point(serveInstall(t), lockMax, 60)
	body = strings.ReplaceAll(body, "/etc/orchestrator", etc)
	out, code := runSh(t, "set -- "+args+"\n"+body, bin)
	got, _ := os.ReadFile(calls)
	return installRun{out: out, code: code, calls: string(got), etc: etc}
}

// A one-time code is spent by the exchange, so nothing that can still stop
// the install may come after it: an unsupported device or a package lock
// that outlasts the wait must leave the code unused, which is what makes
// "run the same command again" true.
func TestInstallDoesNotSpendAOneTimeCodeOnAFailedPreflight(t *testing.T) {
	r := fullInstall(t, "ac_0123456789abcdef", "x86_64", 0, 2700, establishedLine)
	if r.code != 3 {
		t.Fatalf("unsupported architecture: exit %d, want 3:\n%s", r.code, r.out)
	}
	if strings.Contains(r.calls, "adopt/exchange") {
		t.Errorf("the code was exchanged before the architecture check:\n%s", r.calls)
	}

	r = fullInstall(t, "ac_0123456789abcdef", "aarch64", 1000, 300, establishedLine)
	if r.code != 7 || !strings.Contains(r.out, "Run the same command again") {
		t.Fatalf("lock timeout: exit %d, want 7 with the retry advice:\n%s", r.code, r.out)
	}
	if strings.Contains(r.calls, "adopt/exchange") {
		t.Errorf("the code was exchanged before the package was installed:\n%s", r.calls)
	}
}

// On success the code is exchanged once, after the package is installed and
// before the adoption file is written, and the file carries what it
// exchanged into.
func TestInstallExchangesTheCodeAfterThePackageInstall(t *testing.T) {
	r := fullInstall(t, "ac_0123456789abcdef", "aarch64", 0, 2700, establishedLine+"\n"+agentLine("INFO", "command received", "command_id=abc"))
	if r.code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", r.code, r.out)
	}
	deb := strings.Index(r.calls, "/cdn/agent/latest/arm64/deb -o")
	exchange := strings.Index(r.calls, "adopt/exchange")
	if deb < 0 || exchange < 0 || exchange < deb {
		t.Fatalf("want the package download before the exchange:\n%s", r.calls)
	}
	if strings.Count(r.calls, "adopt/exchange") != 1 {
		t.Errorf("the code was exchanged more than once:\n%s", r.calls)
	}
	got, err := os.ReadFile(filepath.Join(r.etc, "node-adoption.txt"))
	if err != nil || !strings.HasPrefix(string(got), "eyJwbGF0Zm9ybV91cmwi") {
		t.Errorf("the adoption file does not carry the exchanged blob: %q %v", got, err)
	}
}

// A refused signature is not a stopped service: it has its own status.
func TestInstallGivesASignatureRefusalItsOwnStatus(t *testing.T) {
	f := newLockFixture(t, 4242, unattendedUpgrade)
	bin := stubs(t, f.dir, 0, false)
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\nexit 22\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := serveInstall(t)
	step := section(t, body, `SIG_MARKER=/etc/orchestrator/.agent-package-sig`, "SKIP_PACKAGE=0")
	script := "set -eu\nSIG_URL=https://example.test/x.sig\nTMP_SIG=" + filepath.Join(f.dir, "sig") + "\n" + step
	out, code := runSh(t, script, bin)
	if code != 10 || !strings.Contains(out, "signature unavailable") {
		t.Fatalf("a missing signature: exit %d, want 10:\n%s", code, out)
	}
}

func serveUninstallFrom(t *testing.T, advertisement, host string) string {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.AdvertisementURL = advertisement
	h := NewHandler(cfg, true, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/uninstall.sh", nil)
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	if err := h.GetUninstallScript(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("GetUninstallScript: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// The usage lines name the server the script came from, with its scheme, as
// install.sh's do; a server with no usable address still serves the script.
func TestUninstallScriptNamesTheServerItCameFrom(t *testing.T) {
	for _, c := range []struct{ adv, host, want string }{
		{"https://orchestrator.example.com/", "", "curl -fsSL https://orchestrator.example.com/uninstall.sh | sudo sh\n"},
		{"orchestrator.example.com", "", "curl -fsSL http://orchestrator.example.com/uninstall.sh | sudo sh\n"},
		{"", "lan.example.com", "curl -fsSL http://lan.example.com/uninstall.sh | sudo sh\n"},
	} {
		body := serveUninstallFrom(t, c.adv, c.host)
		if !strings.Contains(body, c.want) {
			t.Errorf("advertisement %q host %q: the usage line is not %q:\n%s", c.adv, c.host, c.want, section(t, body, "# Usage:", "# Removes"))
		}
		if strings.Contains(body, "__PLATFORM_URL__") {
			t.Errorf("advertisement %q: a placeholder was left in the script", c.adv)
		}
	}
	body := serveUninstallFrom(t, "", "bad host;rm")
	if strings.Contains(body, "bad host") || !strings.Contains(body, "<platform>/uninstall.sh") {
		t.Errorf("an unusable Host header reached the script:\n%s", section(t, body, "# Usage:", "# Removes"))
	}
}

// survivorScan runs the --purge-access survivor count against a scratch
// passwd and shadow file.
func survivorScan(t *testing.T, passwd, shadow string) string {
	t.Helper()
	dir := t.TempDir()
	pw := filepath.Join(dir, "passwd")
	sh := filepath.Join(dir, "shadow")
	if err := os.WriteFile(pw, []byte(strings.ReplaceAll(passwd, "@HOME@", dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sh, []byte(shadow), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"keyed", "locked", "nologin", "expired", "empty"} {
		ssh := filepath.Join(dir, u, ".ssh")
		if err := os.MkdirAll(ssh, 0o700); err != nil {
			t.Fatal(err)
		}
		content := "ssh-ed25519 AAAA " + u + "\n"
		if u == "empty" {
			content = "# nothing\n"
		}
		if err := os.WriteFile(filepath.Join(ssh, "authorized_keys"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body := serveUninstall(t, true)
	step := section(t, body, "# Count logins that survive the removal", "if [ \"$SURVIVORS\" -eq 0 ]")
	step = strings.Replace(step, "PASSWD_FILE=/etc/passwd", "PASSWD_FILE="+pw, 1)
	step = strings.Replace(step, "SHADOW_FILE=/etc/shadow", "SHADOW_FILE="+sh, 1)
	out, code := runSh(t, "set -eu\n"+step+"echo \"survivors=$SURVIVORS\"\n", t.TempDir())
	if code != 0 {
		t.Fatalf("the survivor scan failed (exit %d):\n%s", code, out)
	}
	return out
}

// A login the platform locked (account expiry, nologin shell) keeps its home
// and its authorized_keys, and it is not a way in.
func TestUninstallPurgeAccessDoesNotCountALockedLogin(t *testing.T) {
	passwd := "locked:x:1001:1001::@HOME@/locked:/bin/bash\n" +
		"nologin:x:1002:1002::@HOME@/nologin:/usr/sbin/nologin\n" +
		"expired:x:1003:1003::@HOME@/expired:/bin/bash\n" +
		"empty:x:1004:1004::@HOME@/empty:/bin/bash\n"
	shadow := "locked:!:19000:0:99999:7::1:\nnologin:!:19000:0:99999:7:::\nexpired:*:19000:0:99999:7::19001:\nempty:*:19000:0:99999:7:::\n"
	if out := survivorScan(t, passwd, shadow); !strings.Contains(out, "survivors=0") {
		t.Errorf("a locked, nologin or expired account was counted as a way in:\n%s", out)
	}
	passwd += "keyed:x:1005:1005::@HOME@/keyed:/bin/bash\n"
	shadow += "keyed:!:19000:0:99999:7:::\n"
	if out := survivorScan(t, passwd, shadow); !strings.Contains(out, "survivors=1") {
		t.Errorf("a key-only account with a password locked by '!' is a way in:\n%s", out)
	}
}

// The script disarms only the watchdog the platform configured. A board whose
// owner runs their own watchdog keeps it.
func TestUninstallLeavesAWatchdogThePlatformNeverConfigured(t *testing.T) {
	run := func(withMarker bool) string {
		dir := t.TempDir()
		body := serveUninstall(t, true)
		for _, p := range []string{
			"/etc/systemd/system/watchdog.service.d/10-orchestrator.conf",
			"/etc/systemd/system.conf.d/10-orchestrator-watchdog.conf",
			"/etc/watchdog.conf.orchestrator.bak",
			"/etc/watchdog.conf",
		} {
			body = strings.ReplaceAll(body, p, filepath.Join(dir, filepath.Base(p)))
		}
		if withMarker {
			if err := os.WriteFile(filepath.Join(dir, "10-orchestrator.conf"), []byte("[Unit]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		bin := t.TempDir()
		for name, b := range map[string]string{"id": "echo 0\n", "systemctl": "exit 0\n", "rpi-eeprom-config": "exit 1\n", "vcgencmd": "exit 1\n"} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+b), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		out, code := runSh(t, "set -- --dry-run\n"+body, bin)
		if code != 0 {
			t.Fatalf("dry run: exit %d\n%s", code, out)
		}
		return out
	}
	if out := run(false); strings.Contains(out, "disable --now watchdog.service") {
		t.Errorf("a watchdog the platform never configured was disabled:\n%s", out)
	}
	if out := run(true); !strings.Contains(out, "disable --now watchdog.service") || !strings.Contains(out, "disable --now wd_keepalive.service") {
		t.Errorf("the platform's own watchdog was not disarmed:\n%s", out)
	}
}

// The token can come from a root-only file, so a command that starts the
// install in a unit of its own does not carry it on a command line, which
// the journal records.
func TestInstallReadsTheTokenFromAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("ac_0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	accepted := establishedLine + "\n" + agentLine("INFO", "command received", "command_id=abc")
	r := fullInstallWith(t, "--token-file '"+file+"'", "", "aarch64", 0, 2700, accepted)
	if r.code != 0 || !strings.Contains(r.calls, `{"code":"ac_0123456789abcdef"}`) {
		t.Fatalf("exit %d, want 0 with the file's code exchanged:\n%s\n%s", r.code, r.out, r.calls)
	}

	r = fullInstallWith(t, "--token-file '"+filepath.Join(t.TempDir(), "missing")+"'", "", "aarch64", 0, 2700, accepted)
	if r.code != 2 || !strings.Contains(r.out, "cannot read the token file") {
		t.Fatalf("a missing token file: exit %d, want 2:\n%s", r.code, r.out)
	}
}

// A second start of the same command — a wrapper restarted by the system, or
// run again by hand — must not present a code it already spent: the device
// remembers which code it exchanged, and finishes with the adoption file it
// wrote then instead of failing on the server's "already used".
func TestInstallDoesNotSpendTheSameCodeTwice(t *testing.T) {
	etc := filepath.Join(t.TempDir(), "etc-orchestrator")
	accepted := establishedLine + "\n" + agentLine("INFO", "command received", "command_id=abc")
	first := fullInstallWith(t, "--token 'ac_0123456789abcdef'", etc, "aarch64", 0, 2700, accepted)
	if first.code != 0 {
		t.Fatalf("first run: exit %d:\n%s", first.code, first.out)
	}
	second := fullInstallWith(t, "--token 'ac_0123456789abcdef'", etc, "aarch64", 0, 2700, accepted)
	if second.code != 0 {
		t.Fatalf("second run: exit %d, want 0:\n%s", second.code, second.out)
	}
	if strings.Contains(second.calls, "adopt/exchange") {
		t.Errorf("the second run exchanged the spent code again:\n%s", second.calls)
	}
	third := fullInstallWith(t, "--token 'ac_fedcba9876543210'", etc, "aarch64", 0, 2700, accepted)
	if !strings.Contains(third.calls, "adopt/exchange") {
		t.Errorf("a different code was not exchanged:\n%s", third.calls)
	}
}
