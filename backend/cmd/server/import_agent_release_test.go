package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	local_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/local"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/signing"
)

const testBase = "agent-artifacts"

// A throwaway P-384 key stands in for the hardware tokens; the production
// key set is the command's default and is never needed here.
type testSigner struct {
	priv *ecdsa.PrivateKey
	key  signing.TrustedKey
}

func newTestSigner(t *testing.T, name string) testSigner {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	k, err := signing.ParsePublicKeyPEM(name, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return testSigner{priv: priv, key: k}
}

func (s testSigner) sign(t *testing.T, data []byte) []byte {
	t.Helper()
	d := sha512.Sum384(data)
	sig, err := ecdsa.SignASN1(rand.Reader, s.priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// buildDeb writes a minimal but real .deb: an ar archive with
// debian-binary, control.tar.gz (./control) and data.tar.gz.
func buildDeb(t *testing.T, pkg, version, arch, payload string) []byte {
	t.Helper()
	tgz := func(name, body string) []byte {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	control := fmt.Sprintf("Package: %s\nVersion: %s\nSection: default\nArchitecture: %s\nMaintainer: Test\nDescription: test\n multi-line\n", pkg, version, arch)
	members := []struct {
		name string
		data []byte
	}{
		{"debian-binary", []byte("2.0\n")},
		{"control.tar.gz", tgz("./control", control)},
		{"data.tar.gz", tgz("./usr/bin/"+pkg, payload)},
	}
	var out bytes.Buffer
	out.WriteString("!<arch>\n")
	for _, m := range members {
		fmt.Fprintf(&out, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", m.name, "0", "0", "0", "100644", len(m.data))
		out.Write(m.data)
		if len(m.data)%2 == 1 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

type releaseFixture struct {
	dir   string
	files map[string][]byte
}

// writeRelease lays out a release directory as `gh release download` would:
// agent and watcher .deb with their .sig, plus an SBOM.
func writeRelease(t *testing.T, s testSigner, version string) releaseFixture {
	t.Helper()
	dir := t.TempDir()
	f := releaseFixture{dir: dir, files: map[string][]byte{}}
	for _, pkg := range []string{"orchestrator-agent", "orchestrator-watcher"} {
		deb := buildDeb(t, pkg, version, "arm64", pkg+" "+version)
		name := fmt.Sprintf("%s_%s_linux_arm64.deb", pkg, version)
		f.put(t, name, deb)
		f.put(t, name+".sig", s.sign(t, deb))
	}
	f.put(t, "orchestrator-agent-v"+version+".spdx.json", []byte(`{"spdxVersion":"SPDX-2.3"}`))
	return f
}

func (f releaseFixture) put(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	f.files[name] = data
}

type harness struct {
	storeDir string
	store    storage.StorageClient
	im       *releaseImporter
	out      *bytes.Buffer
}

func newHarness(t *testing.T, keys ...signing.TrustedKey) *harness {
	t.Helper()
	dir := t.TempDir()
	local, err := local_storage.NewLocalStorageClient(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{storeDir: dir, store: local, out: &bytes.Buffer{}}
	h.im = &releaseImporter{
		store:    local,
		basePath: testBase,
		keys:     keys,
		stdout:   h.out,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return h
}

func (h *harness) run(t *testing.T, opts importOptions) error {
	t.Helper()
	h.im.workDir = t.TempDir()
	return h.im.run(context.Background(), opts)
}

// stored lists the objects under the agent CDN base path.
func (h *harness) stored(t *testing.T) []string {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(h.storeDir, testBase))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

func (h *harness) read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.storeDir, testBase, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func versionedNames(v string) []string {
	return []string{
		"orchestrator-agent_" + v + "_linux_arm64.deb",
		"orchestrator-agent_" + v + "_linux_arm64.deb.sig",
		"orchestrator-watcher_" + v + "_linux_arm64.deb",
		"orchestrator-watcher_" + v + "_linux_arm64.deb.sig",
	}
}

func TestImportValidRelease(t *testing.T) {
	s := newTestSigner(t, "test-signing.pem")
	rel := writeRelease(t, s, "0.7.9")
	h := newHarness(t, s.key)

	if err := h.run(t, importOptions{paths: []string{rel.dir}}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := h.stored(t); !reflect.DeepEqual(got, versionedNames("0.7.9")) {
		t.Fatalf("stored %v, want exactly %v (no latest without --set-latest)", got, versionedNames("0.7.9"))
	}
	for _, n := range versionedNames("0.7.9") {
		if !bytes.Equal(h.read(t, n), rel.files[n]) {
			t.Errorf("%s differs from the release asset", n)
		}
	}
	// What GET /cdn/agent-versions serves.
	lister := h.store.(interface {
		ListAgentVersions(string) ([]string, error)
	})
	if v, err := lister.ListAgentVersions(testBase); err != nil || !reflect.DeepEqual(v, []string{"0.7.9"}) {
		t.Fatalf("listing: %v, %v", v, err)
	}
	out := h.out.String()
	for _, want := range []string{"orchestrator-watcher", "arm64", "test-signing.pem:" + s.key.Fingerprint[:16], testBase + "/orchestrator-agent_0.7.9_linux_arm64.deb", "latest unchanged"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestImportAcceptsAVPrefixedFileNameAndStoresTheBareVersion(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	dir := t.TempDir()
	deb := buildDeb(t, "orchestrator-agent", "0.7.9", "arm64", "x")
	name := "orchestrator-agent_v0.7.9_linux_arm64.deb"
	if err := os.WriteFile(filepath.Join(dir, name), deb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".sig"), s.sign(t, deb), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, s.key)
	if err := h.run(t, importOptions{paths: []string{filepath.Join(dir, name)}}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := h.stored(t); !reflect.DeepEqual(got, versionedNames("0.7.9")[:2]) {
		t.Fatalf("stored %v", got)
	}
}

func assertRefusedNothingWritten(t *testing.T, h *harness, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatal("want a refusal, got success")
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("error %q does not say %q", err, contains)
	}
	if got := h.stored(t); len(got) != 0 {
		t.Fatalf("a refusal wrote %v", got)
	}
}

func TestImportRefusesATamperedPackage(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	rel := writeRelease(t, s, "0.7.9")
	name := "orchestrator-watcher_0.7.9_linux_arm64.deb"
	tampered := append([]byte{}, rel.files[name]...)
	tampered[len(tampered)-3] ^= 0xff
	rel.put(t, name, tampered)
	h := newHarness(t, s.key)
	assertRefusedNothingWritten(t, h, h.run(t, importOptions{paths: []string{rel.dir}}), "does not match any trusted key")
}

func TestImportRefusesAMissingSignature(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	rel := writeRelease(t, s, "0.7.9")
	if err := os.Remove(filepath.Join(rel.dir, "orchestrator-agent_0.7.9_linux_arm64.deb.sig")); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, s.key)
	assertRefusedNothingWritten(t, h, h.run(t, importOptions{paths: []string{rel.dir}}), "no detached signature")
}

func TestImportRefusesAnUntrustedSigner(t *testing.T) {
	trusted := newTestSigner(t, "trusted.pem")
	stranger := newTestSigner(t, "stranger.pem")
	rel := writeRelease(t, stranger, "0.7.9")
	h := newHarness(t, trusted.key)
	assertRefusedNothingWritten(t, h, h.run(t, importOptions{paths: []string{rel.dir}}), "does not match any trusted key")
}

func TestImportRefusesUnknownNames(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	for _, bad := range []string{
		"orchestrator-agent_latest_linux_arm64.deb",
		"orchestrator-observer_0.7.9_linux_arm64.deb",
		"orchestrator-agent_0.7.9_linux_riscv64.deb",
		"orchestrator-agent_0.7.9_linux_arm64.rpm.sig.deb",
	} {
		t.Run(bad, func(t *testing.T) {
			rel := writeRelease(t, s, "0.7.9")
			rel.put(t, bad, []byte("x"))
			h := newHarness(t, s.key)
			assertRefusedNothingWritten(t, h, h.run(t, importOptions{paths: []string{rel.dir}}), "not an importable name")
		})
	}
	t.Run("an explicitly named stranger file", func(t *testing.T) {
		rel := writeRelease(t, s, "0.7.9")
		rel.put(t, "notes.txt", []byte("x"))
		h := newHarness(t, s.key)
		err := h.run(t, importOptions{paths: []string{
			filepath.Join(rel.dir, "orchestrator-agent_0.7.9_linux_arm64.deb"),
			filepath.Join(rel.dir, "notes.txt"),
		}})
		assertRefusedNothingWritten(t, h, err, "not part of an agent release")
	})
}

// The signature covers bytes, not the name: a validly signed 0.7.8 renamed
// 0.7.9 must not be published as 0.7.9.
func TestImportRefusesARenamedSignedPackage(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	dir := t.TempDir()
	deb := buildDeb(t, "orchestrator-agent", "0.7.8", "arm64", "old")
	name := "orchestrator-agent_0.7.9_linux_arm64.deb"
	if err := os.WriteFile(filepath.Join(dir, name), deb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".sig"), s.sign(t, deb), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, s.key)
	assertRefusedNothingWritten(t, h, h.run(t, importOptions{paths: []string{dir}}), "the signed package says orchestrator-agent 0.7.8")
}

func TestImportChecksums(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	sum := func(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }

	t.Run("a matching checksum file passes", func(t *testing.T) {
		rel := writeRelease(t, s, "0.7.9")
		var lines []string
		for _, n := range versionedNames("0.7.9") {
			lines = append(lines, sum(rel.files[n])+"  "+n)
		}
		rel.put(t, "checksums.txt", []byte(strings.Join(lines, "\n")+"\n"))
		h := newHarness(t, s.key)
		if err := h.run(t, importOptions{paths: []string{rel.dir}}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a mismatching .sha256 is refused", func(t *testing.T) {
		rel := writeRelease(t, s, "0.7.9")
		rel.put(t, "orchestrator-agent_0.7.9_linux_arm64.deb.sha256", []byte(strings.Repeat("0", 64)+"  orchestrator-agent_0.7.9_linux_arm64.deb\n"))
		h := newHarness(t, s.key)
		assertRefusedNothingWritten(t, h, h.run(t, importOptions{paths: []string{rel.dir}}), "checksum mismatch")
	})
}

func TestImportRefusesAnOversizedSignature(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	rel := writeRelease(t, s, "0.7.9")
	rel.put(t, "orchestrator-agent_0.7.9_linux_arm64.deb.sig", bytes.Repeat([]byte{1}, maxImportSignatureByte+1))
	h := newHarness(t, s.key)
	assertRefusedNothingWritten(t, h, h.run(t, importOptions{paths: []string{rel.dir}}), "limit")
}

func TestImportRefusesAnExistingVersionWithoutReplace(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	rel := writeRelease(t, s, "0.7.9")
	h := newHarness(t, s.key)
	if err := h.run(t, importOptions{paths: []string{rel.dir}}); err != nil {
		t.Fatal(err)
	}
	before := h.read(t, "orchestrator-agent_0.7.9_linux_arm64.deb")

	// A different, validly signed build of the same version.
	deb := buildDeb(t, "orchestrator-agent", "0.7.9", "arm64", "rebuilt")
	rel.put(t, "orchestrator-agent_0.7.9_linux_arm64.deb", deb)
	rel.put(t, "orchestrator-agent_0.7.9_linux_arm64.deb.sig", s.sign(t, deb))

	err := h.run(t, importOptions{paths: []string{rel.dir}})
	if err == nil || !strings.Contains(err.Error(), "already published") {
		t.Fatalf("want already published, got %v", err)
	}
	if !bytes.Equal(h.read(t, "orchestrator-agent_0.7.9_linux_arm64.deb"), before) {
		t.Fatal("a refused import changed a published object")
	}
	if err := h.run(t, importOptions{paths: []string{rel.dir}, replace: true}); err != nil {
		t.Fatalf("--replace: %v", err)
	}
	if !bytes.Equal(h.read(t, "orchestrator-agent_0.7.9_linux_arm64.deb"), deb) {
		t.Fatal("--replace did not overwrite")
	}
}

func TestImportSetLatestWritesExactlyTheLatestObjects(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	rel := writeRelease(t, s, "0.7.9")
	h := newHarness(t, s.key)
	if err := h.run(t, importOptions{paths: []string{rel.dir}, setLatest: true}); err != nil {
		t.Fatal(err)
	}
	// An arm64-only release writes arm64's marker and never the legacy,
	// architecture-agnostic one, which would claim amd64's `latest` moved.
	want := append(append(versionedNames("0.7.9"), versionedNames("latest")...), "latest.arm64.version")
	got := h.stored(t)
	wantSorted := append([]string{}, want...)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(got, wantSorted) {
		t.Fatalf("stored %v, want %v", got, wantSorted)
	}
	for i, n := range versionedNames("latest") {
		if !bytes.Equal(h.read(t, n), h.read(t, versionedNames("0.7.9")[i])) {
			t.Errorf("%s is not a byte copy of its versioned twin", n)
		}
	}

	// An older version imported for a rollback leaves latest alone by default.
	old := writeRelease(t, s, "0.7.8")
	if err := h.run(t, importOptions{paths: []string{old.dir}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h.read(t, "orchestrator-agent_latest_linux_arm64.deb"), rel.files["orchestrator-agent_0.7.9_linux_arm64.deb"]) {
		t.Fatal("importing an older version without --set-latest moved latest")
	}
}

// failingStore fails the FIRST StoreWithOptions whose key ends in match;
// later writes, including the restore, go through.
type failingStore struct {
	storage.StorageClient
	match string
	fired bool
}

func (f *failingStore) StoreWithOptions(ctx context.Context, name string, r io.Reader, o *storage.StorageOptions) (string, error) {
	if !f.fired && strings.HasSuffix(name, "/"+f.match) {
		f.fired = true
		return "", errors.New("injected storage failure")
	}
	return f.StorageClient.StoreWithOptions(ctx, name, r, o)
}

func TestImportStorageFailureLeavesNoPartialLatest(t *testing.T) {
	s := newTestSigner(t, "k.pem")

	t.Run("a versioned write fails: latest is never touched", func(t *testing.T) {
		prev := writeRelease(t, s, "0.7.8")
		h := newHarness(t, s.key)
		if err := h.run(t, importOptions{paths: []string{prev.dir}, setLatest: true}); err != nil {
			t.Fatal(err)
		}
		h.im.store = &failingStore{StorageClient: h.store, match: "orchestrator-watcher_0.7.9_linux_arm64.deb"}
		rel := writeRelease(t, s, "0.7.9")
		err := h.run(t, importOptions{paths: []string{rel.dir}, setLatest: true})
		if err == nil || !strings.Contains(err.Error(), "`latest` was not touched") {
			t.Fatalf("want a storage failure, got %v", err)
		}
		for _, n := range versionedNames("latest") {
			v := strings.Replace(n, "latest", "0.7.8", 1)
			if !bytes.Equal(h.read(t, n), prev.files[v]) {
				t.Errorf("%s moved although a versioned write failed", n)
			}
		}
	})

	t.Run("a latest write fails: every latest object is put back", func(t *testing.T) {
		prev := writeRelease(t, s, "0.7.8")
		h := newHarness(t, s.key)
		if err := h.run(t, importOptions{paths: []string{prev.dir}, setLatest: true}); err != nil {
			t.Fatal(err)
		}
		// The agent's latest pair is written, then the watcher's latest
		// .deb (after its .sig) fails.
		h.im.store = &failingStore{StorageClient: h.store, match: "orchestrator-watcher_latest_linux_arm64.deb"}
		rel := writeRelease(t, s, "0.7.9")
		err := h.run(t, importOptions{paths: []string{rel.dir}, setLatest: true})
		if err == nil || !strings.Contains(err.Error(), "`latest` is as it was") {
			t.Fatalf("want a restored latest, got %v", err)
		}
		for _, n := range versionedNames("latest") {
			v := strings.Replace(n, "latest", "0.7.8", 1)
			if !bytes.Equal(h.read(t, n), prev.files[v]) {
				t.Errorf("%s was left moved after a failed latest step", n)
			}
		}
		// The versioned objects of the new release are in place.
		for _, n := range versionedNames("0.7.9") {
			if !bytes.Equal(h.read(t, n), rel.files[n]) {
				t.Errorf("%s missing", n)
			}
		}
	})
}

func TestImportArgumentParsing(t *testing.T) {
	if _, present, _ := parseImportArgs([]string{"--init-secrets", "/x"}); present {
		t.Fatal("another command's arguments must not be claimed")
	}
	opts, present, err := parseImportArgs([]string{importAgentReleaseFlag, "/r", flagSetLatest, "/s.deb"})
	if !present || err != nil || !opts.setLatest || opts.replace || !reflect.DeepEqual(opts.paths, []string{"/r", "/s.deb"}) {
		t.Fatalf("got %+v %v %v", opts, present, err)
	}
	opts, _, err = parseImportArgs([]string{importAgentReleaseFlag + "=/r", flagReplace})
	if err != nil || !opts.replace || !reflect.DeepEqual(opts.paths, []string{"/r"}) {
		t.Fatalf("= form: %+v %v", opts, err)
	}
	for _, bad := range [][]string{
		{importAgentReleaseFlag},
		{importAgentReleaseFlag, "--force", "/r"},
		{importAgentReleaseFlag + "="},
	} {
		var stderr bytes.Buffer
		handled, code := importAgentReleaseCommand(bad, io.Discard, &stderr)
		if !handled || code != 2 || !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("%v: handled=%v code=%d stderr=%q", bad, handled, code, stderr.String())
		}
	}
	if handled, _ := importAgentReleaseCommand([]string{}, io.Discard, io.Discard); handled {
		t.Fatal("a plain server start must not be handled")
	}
}

func TestReadDebControl(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.deb")
	if err := os.WriteFile(p, buildDeb(t, "orchestrator-watcher", "1.2.3-rc.1", "amd64", "odd"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, v, arch, err := readDebControl(p)
	if err != nil || pkg != "orchestrator-watcher" || v != "1.2.3-rc.1" || arch != "amd64" {
		t.Fatalf("got %q %q %q %v", pkg, v, arch, err)
	}
	if err := os.WriteFile(p, []byte("not a deb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readDebControl(p); err == nil {
		t.Fatal("a non-deb must be refused")
	}
}

// A release carrying both architectures
// writes both per-arch markers and, being true for every architecture, the
// legacy one; a later arm64-only release moves arm64's marker alone.
func TestImportSetLatestWritesOneMarkerPerArchitecture(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	rel := writeRelease(t, s, "0.7.9")
	for _, pkg := range []string{"orchestrator-agent", "orchestrator-watcher"} {
		deb := buildDeb(t, pkg, "0.7.9", "amd64", pkg+" 0.7.9 amd64")
		name := pkg + "_0.7.9_linux_amd64.deb"
		rel.put(t, name, deb)
		rel.put(t, name+".sig", s.sign(t, deb))
	}
	h := newHarness(t, s.key)
	if err := h.run(t, importOptions{paths: []string{rel.dir}, setLatest: true}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"latest.arm64.version", "latest.amd64.version", "latest.version"} {
		if got := strings.TrimSpace(string(h.read(t, m))); got != "0.7.9" {
			t.Errorf("%s = %q, want 0.7.9", m, got)
		}
	}

	next := writeRelease(t, s, "0.8.0") // arm64 only
	if err := h.run(t, importOptions{paths: []string{next.dir}, setLatest: true}); err != nil {
		t.Fatal(err)
	}
	for m, want := range map[string]string{
		"latest.arm64.version": "0.8.0",
		"latest.amd64.version": "0.7.9",
		"latest.version":       "0.7.9",
	} {
		if got := strings.TrimSpace(string(h.read(t, m))); got != want {
			t.Errorf("after an arm64-only release %s = %q, want %s", m, got, want)
		}
	}
}
