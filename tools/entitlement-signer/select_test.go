package main

import (
	"bytes"
	"crypto"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixture is one signed document, built like the backend service test's
// harness.sign: these defaults are its defaults.
func fixtureDoc(mutate func(*document)) document {
	d := document{
		Version:    version,
		CustomerID: "customer-1",
		DeviceCap:  100,
		IssuedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Kind:       "evaluation",
		GraceDays:  30,
	}
	if mutate != nil {
		mutate(&d)
	}
	return d
}

func cands(docs ...document) []candidate {
	out := make([]candidate, len(docs))
	for i, d := range docs {
		out[i] = candidate{name: d.CustomerID, doc: d}
	}
	return out
}

// The server's selection rule (services/entitlement resolveUncached), pinned
// with the fixtures of its own tests in
// backend/pkg/webapp/application/services/entitlement/service_test.go —
// TestNewestValidFileWins, TestAllLapsedShowsTheNewest and
// TestBootFileIsACandidate — offered in the order the server sees them.
func TestSelectInForceMirrorsTheServer(t *testing.T) {
	subscription := fixtureDoc(func(d *document) {
		d.CustomerID = "subscription"
		d.Kind = "subscription"
		d.DeviceCap = 500
		d.IssuedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	shortTrial := fixtureDoc(func(d *document) {
		d.CustomerID = "trial"
		d.DeviceCap = 1000
		d.IssuedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		d.GraceDays = 0
	})
	upgrade := fixtureDoc(func(d *document) {
		d.CustomerID = "upgrade"
		d.Kind = "subscription"
		d.DeviceCap = 0
		d.IssuedAt = time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	})
	may := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// TestNewestValidFileWins: pasted in the wrong order, the newest issued wins.
	if got := selectInForce(cands(upgrade, subscription), may); got == nil || got.name != "upgrade" || effectiveCapAt(got.doc, may) != 0 {
		t.Errorf("newest valid: got %+v, want the upgrade, unlimited", got)
	}
	// ... and a lapsed trial issued between them changes nothing.
	if got := selectInForce(cands(upgrade, subscription, shortTrial), may); got == nil || got.name != "upgrade" {
		t.Errorf("a lapsed file displaced a live one: %+v", got)
	}
	if got := selectInForce(cands(shortTrial, subscription), may); got == nil || got.name != "subscription" {
		t.Errorf("a lapsed file beat a live one offered after it: %+v", got)
	}

	// TestAllLapsedShowsTheNewest: every file lapsed, the newest of them.
	jan2027 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	older := fixtureDoc(func(d *document) { d.CustomerID = "older" })
	newer := fixtureDoc(func(d *document) {
		d.CustomerID = "newer"
		d.IssuedAt = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	})
	got := selectInForce(cands(newer, older), jan2027)
	if got == nil || got.name != "newer" || effectiveCapAt(got.doc, jan2027) != freeDeviceCap {
		t.Errorf("all lapsed: got %+v, want the newer one at the free cap", got)
	}

	// TestBootFileIsACandidate: the boot file wins over nothing and loses to
	// a newer paste.
	june := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	boot := fixtureDoc(func(d *document) {
		d.CustomerID = "boot"
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	pasted := fixtureDoc(func(d *document) {
		d.CustomerID = "pasted"
		d.IssuedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	if got := selectInForce(cands(boot), june); got == nil || got.name != "boot" {
		t.Errorf("the boot file alone is not in force: %+v", got)
	}
	if got := selectInForce(cands(boot, pasted), june); got == nil || got.name != "pasted" {
		t.Errorf("a newer paste did not win over the boot file: %+v", got)
	}
	// A tie keeps the earlier one: the server's `After` is strict.
	twin := boot
	twin.CustomerID = "twin"
	if got := selectInForce(cands(boot, twin), june); got == nil || got.name != "boot" {
		t.Errorf("a tie did not keep the earlier file: %+v", got)
	}
	if selectInForce(nil, june) != nil {
		t.Error("no candidate must select nothing")
	}
}

type selectHarness struct {
	t    *testing.T
	priv crypto.Signer
	pub  string
	dir  string
}

func newSelectHarness(t *testing.T) *selectHarness {
	t.Helper()
	keyPath, pub := writeTestKey(t)
	priv, err := loadPrivateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return &selectHarness{t: t, priv: priv, pub: writeTestPub(t, pub), dir: t.TempDir()}
}

func (h *selectHarness) form(d document) string {
	h.t.Helper()
	payload, err := encode(d)
	if err != nil {
		h.t.Fatal(err)
	}
	return formPrefix + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(mustSign(h.t, h.priv, signingInput(payload)))
}

func (h *selectHarness) file(name, content string) string {
	h.t.Helper()
	p := filepath.Join(h.dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *selectHarness) run(now time.Time, args ...string) (string, error) {
	var out bytes.Buffer
	err := verifyCmd(append([]string{"--pub", h.pub}, args...), strings.NewReader(""), &out, now)
	return out.String(), err
}

// The preflight's blind spot: the boot file covers the fleet, but a
// newer applied file with a smaller cap is what the server puts in force.
// Verifying the boot file alone passes; --select over the boot file and the
// rows judges the file in force and refuses.
func TestVerifySelectJudgesTheFileInForce(t *testing.T) {
	h := newSelectHarness(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	hosted := h.file("boot", h.form(fixtureDoc(func(d *document) {
		d.CustomerID, d.Kind, d.DeviceCap = "hosted", "hosted", 0
		d.ExpiresAt = time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	})))
	smaller := h.file("row-1", h.form(fixtureDoc(func(d *document) {
		d.CustomerID, d.Kind, d.DeviceCap = "smaller", "subscription", 30
		d.IssuedAt = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})))
	lapsedNewer := h.file("row-lapsed", h.form(fixtureDoc(func(d *document) {
		d.CustomerID, d.DeviceCap = "lapsed", 30
		d.IssuedAt = time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)
		d.GraceDays = 0
	})))
	forged := h.file("row-forged", "entl1.bm90LWEtcGF5bG9hZA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	empty := h.file("boot-empty", "")

	// The control: the boot file alone, as the old preflight asked.
	if _, err := h.run(now, "--in", hosted, "--require-active", "--min-cap", "77"); err != nil {
		t.Fatalf("the boot file alone must pass (the old preflight's answer): %v", err)
	}
	out, err := h.run(now, "--select", "--in", hosted, "--in", smaller, "--require-active", "--min-cap", "77")
	if err == nil || !strings.Contains(err.Error(), "device_cap 30 does not cover the 77") {
		t.Fatalf("a newer, smaller applied file in force must fail the preflight: err %v\n%s", err, out)
	}
	if !strings.Contains(out, "IN FORCE (the server's selection): row-1") {
		t.Errorf("the selection is not reported:\n%s", out)
	}
	// A lapsed newer file and a forged one do not displace the boot file.
	out, err = h.run(now, "--select", "--in", hosted, "--in", lapsedNewer, "--in", forged, "--require-active", "--min-cap", "77")
	if err != nil || !strings.Contains(out, "IN FORCE (the server's selection): boot") || !strings.Contains(out, "row-forged: REJECTED") {
		t.Errorf("lapsed and rejected files must not displace the boot file: %v\n%s", err, out)
	}
	// An empty boot file is no file, as on the server; with no candidate the
	// server runs on the free cap, and the preflight fails.
	out, err = h.run(now, "--select", "--in", empty, "--in", smaller, "--min-cap", "20")
	if err != nil || !strings.Contains(out, "boot-empty: empty, not a candidate") {
		t.Errorf("an empty boot file with a covering row: %v\n%s", err, out)
	}
	if _, err := h.run(now, "--select", "--in", empty, "--in", forged); err == nil || !strings.Contains(err.Error(), "free cap") {
		t.Errorf("no candidate must fail naming the free cap: %v", err)
	}
	if _, err := h.run(now, "--in", hosted, "--in", smaller); err == nil || !strings.Contains(err.Error(), "--select") {
		t.Errorf("several --in without --select must be refused: %v", err)
	}
}

// The server parses each embedded key file on its own and trusts NO key
// when one fails (loadTrusted). verify does the same with every --pub, naming
// the file; concatenated, the bad file would have vanished.
func TestEveryKeyFileMustParse(t *testing.T) {
	h := newSelectHarness(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	in := h.file("boot", h.form(fixtureDoc(func(d *document) { d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) })))
	emptyKey := h.file("entitlement-signing-2.pem", "")
	_, err := h.run(now, "--pub", emptyKey, "--in", in)
	if err == nil || !strings.Contains(err.Error(), "key file entitlement-signing-2.pem") || !strings.Contains(err.Error(), "trusts no key") {
		t.Fatalf("an empty second key file must refuse, naming it: %v", err)
	}
	notAKey := h.file("entitlement-signing-3.pem", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
	if _, err := h.run(now, "--select", "--pub", notAKey, "--in", in); err == nil || !strings.Contains(err.Error(), "entitlement-signing-3.pem") {
		t.Errorf("a key file holding another PEM type must refuse, naming it: %v", err)
	}
	// The control: the same key material concatenated passes, which is the
	// blind spot per-file parsing closes.
	good, _ := os.ReadFile(h.pub)
	both := h.file("trusted.pem", string(good)) // the good file and the empty one concatenated: the empty adds nothing
	if err := verifyCmd([]string{"--pub", both, "--in", in}, strings.NewReader(""), &bytes.Buffer{}, now); err != nil {
		t.Errorf("the concatenation control did not pass: %v", err)
	}
}

// The server refuses a paste longer than MaxFormLength before checking
// anything; a valid file padded past it (whitespace the parser would
// otherwise compact away) is refused here too.
func TestVerifyRefusesAFormOverTheServerBound(t *testing.T) {
	h := newSelectHarness(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	form := h.form(fixtureDoc(func(d *document) { d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }))
	atBound := h.file("at", form+strings.Repeat("\n", maxFormLength-len(form)))
	if _, err := h.run(now, "--in", atBound); err != nil {
		t.Fatalf("a file exactly at the bound must verify: %v", err)
	}
	over := h.file("over", form+strings.Repeat("\n", maxFormLength-len(form)+1))
	if _, err := h.run(now, "--in", over); err == nil || !strings.Contains(err.Error(), "over the server's bound") {
		t.Errorf("a file over the bound must be refused: %v", err)
	}
	out, err := h.run(now, "--select", "--in", over)
	if err == nil || !strings.Contains(out, "over: REJECTED") {
		t.Errorf("--select must reject a file over the bound as the server does: %v\n%s", err, out)
	}
}

// The signer's copies of the server's bounds are equal to the server's,
// read from the backend's source (both live in the public tree).
func TestBoundsMatchTheServerSource(t *testing.T) {
	dir := filepath.Join("..", "..", "backend", "pkg", "webapp", "domain", "entitlement")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no backend source beside this module (%v)", err)
	}
	for _, c := range []struct {
		file, name string
		local      int
	}{
		{"encoding.go", "MaxFormLength", maxFormLength},
		{"document.go", "FreeDeviceCap", freeDeviceCap},
	} {
		src, err := os.ReadFile(filepath.Join(dir, c.file))
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`(?m)^\s*(?:const\s+)?` + c.name + `\s*=\s*([0-9 *]+?)\s*$`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s: no `%s = <int expression>` found", c.file, c.name)
		}
		value := 1
		for _, f := range strings.Split(string(m[1]), "*") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				t.Fatalf("%s: %s = %q is not a product of integers", c.file, c.name, m[1])
			}
			value *= n
		}
		if value != c.local {
			t.Errorf("the server's %s is %d, the signer's copy %d", c.name, value, c.local)
		}
	}
}
