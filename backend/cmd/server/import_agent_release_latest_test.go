package main

import (
	"bytes"
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/agentrelease"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/signing"
)

// nthWriteFails fails the n-th StoreWithOptions call (1-based) and lets every
// other write through. With cancel set, the failing write also cancels the
// import's context, and every later write made under a cancelled context
// fails — the shape of an import that ran into its own deadline.
type nthWriteFails struct {
	storage.StorageClient
	n, calls int
	cancel   context.CancelFunc
}

func (f *nthWriteFails) StoreWithOptions(ctx context.Context, name string, r io.Reader, o *storage.StorageOptions) (string, error) {
	f.calls++
	if f.calls == f.n {
		if f.cancel != nil {
			f.cancel()
			return "", context.DeadlineExceeded
		}
		return "", errors.New("injected storage failure")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return f.StorageClient.StoreWithOptions(ctx, name, r, o)
}

// pairVerifies reports whether a stored package and its stored signature
// belong together, which is what every device checks before installing.
func pairVerifies(t *testing.T, h *harness, key signing.TrustedKey, deb string) bool {
	t.Helper()
	pkg, err := os.ReadFile(filepath.Join(h.storeDir, testBase, deb))
	if err != nil {
		return false
	}
	sig, err := os.ReadFile(filepath.Join(h.storeDir, testBase, deb+".sig"))
	if err != nil {
		return false
	}
	d := sha512.Sum384(pkg)
	_, err = signing.VerifyDigest([]signing.TrustedKey{key}, d, sig)
	return err == nil
}

// A failed --replace never leaves a published version whose package and
// signature disagree: whichever write fails, the pair is consistent afterwards
// or the error says it may not be.
func TestImportReplaceFailureKeepsThePublishedPairConsistent(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	for _, deadline := range []bool{false, true} {
		for n := 1; n <= 9; n++ {
			t.Run(fmt.Sprintf("write %d deadline=%v", n, deadline), func(t *testing.T) {
				first := writeRelease(t, s, "0.7.9")
				h := newHarness(t, s.key)
				if err := h.run(t, importOptions{paths: []string{first.dir}, setLatest: true}); err != nil {
					t.Fatal(err)
				}
				// A different, validly signed build of the same version.
				rebuilt := writeRelease(t, s, "0.7.9")
				for _, pkg := range []string{"orchestrator-agent", "orchestrator-watcher"} {
					deb := buildDeb(t, pkg, "0.7.9", "arm64", pkg+" rebuilt")
					name := pkg + "_0.7.9_linux_arm64.deb"
					rebuilt.put(t, name, deb)
					rebuilt.put(t, name+".sig", s.sign(t, deb))
				}

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fake := &nthWriteFails{StorageClient: h.store, n: n}
				if deadline {
					fake.cancel = cancel
				}
				h.im.store = fake
				h.im.workDir = t.TempDir()
				err := h.im.run(ctx, importOptions{paths: []string{rebuilt.dir}, replace: true, setLatest: true})
				if err == nil {
					t.Fatal("want the injected failure")
				}
				saysBroken := strings.Contains(err.Error(), "may not match") || strings.Contains(err.Error(), "checked by hand")
				for _, v := range []string{"0.7.9", "latest"} {
					for _, pkg := range []string{"orchestrator-agent", "orchestrator-watcher"} {
						deb := pkg + "_" + v + "_linux_arm64.deb"
						if !pairVerifies(t, h, s.key, deb) && !saysBroken {
							t.Errorf("%s and its signature disagree after a failed import, and the error does not say so: %v", deb, err)
						}
					}
				}
				if saysBroken {
					t.Errorf("a local store restored nothing: %v", err)
				}
			})
		}
	}
}

// `latest` is what a one-click update installs; the marker the import writes
// beside it is what the fleet is compared against, not the newest listing.
func TestImportLatestMarkerDecidesWhatLatestIs(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	h := newHarness(t, s.key)

	// Before any import: no marker, the newest listed version answers.
	old := writeRelease(t, s, "0.7.8")
	if err := h.run(t, importOptions{paths: []string{old.dir}}); err != nil {
		t.Fatal(err)
	}
	if v, err := agentrelease.NewSource(h.store, testBase).Latest(context.Background()); err != nil || v != "0.7.8" {
		t.Fatalf("no marker: Latest = %q, %v; want the newest listed version", v, err)
	}

	rel := writeRelease(t, s, "0.7.9")
	if err := h.run(t, importOptions{paths: []string{rel.dir}, setLatest: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(h.read(t, "latest.arm64.version"))); got != "0.7.9" {
		t.Fatalf("marker holds %q, want 0.7.9", got)
	}

	// An import without --set-latest adds a newer version; `latest` and the
	// marker stay, and the fleet is still compared against 0.7.9.
	h.out.Reset()
	newer := writeRelease(t, s, "0.8.0")
	if err := h.run(t, importOptions{paths: []string{newer.dir}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := agentrelease.NewSource(h.store, testBase).Latest(context.Background()); v != "0.7.9" {
		t.Fatalf("after an import-only run Latest = %q, want 0.7.9 (what `latest` installs)", v)
	}
	out := h.out.String()
	for _, want := range []string{"latest unchanged", "keep installing 0.7.9", "--set-latest"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestImportWithoutAnyLatestSaysUpdatesWillFail(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	h := newHarness(t, s.key)
	rel := writeRelease(t, s, "0.7.9")
	if err := h.run(t, importOptions{paths: []string{rel.dir}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "no `latest` is published for arm64") {
		t.Fatalf("summary:\n%s", h.out.String())
	}
}

// nfpm writes a pre-release as `0.8.0~rc.1` into the control file while the
// file name says `0.8.0-rc.1`.
func TestImportAcceptsAPreReleaseWhoseControlUsesATilde(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	dir := t.TempDir()
	deb := buildDeb(t, "orchestrator-agent", "0.8.0~rc.1", "arm64", "rc")
	name := "orchestrator-agent_0.8.0-rc.1_linux_arm64.deb"
	if err := os.WriteFile(filepath.Join(dir, name), deb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".sig"), s.sign(t, deb), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, s.key)
	if err := h.run(t, importOptions{paths: []string{dir}}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !bytes.Equal(h.read(t, name), deb) {
		t.Fatal("pre-release not stored under its name")
	}
	// A tilde is not a licence to rename: a different version still refuses.
	dir2 := t.TempDir()
	other := buildDeb(t, "orchestrator-agent", "0.8.0~rc.2", "arm64", "rc2")
	if err := os.WriteFile(filepath.Join(dir2, name), other, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, name+".sig"), s.sign(t, other), 0o644); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, s.key)
	if err := h2.run(t, importOptions{paths: []string{dir2}}); err == nil || !strings.Contains(err.Error(), "the signed package says") {
		t.Fatalf("a renamed pre-release was accepted: %v", err)
	}
}

// --set-latest with a release that carries one architecture moves only that
// architecture's `latest`, and the summary says the other one stayed behind.
func TestImportSetLatestNamesAnArchitectureLeftBehind(t *testing.T) {
	s := newTestSigner(t, "k.pem")
	h := newHarness(t, s.key)

	both := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		deb := buildDeb(t, "orchestrator-agent", "0.7.8", arch, arch)
		name := "orchestrator-agent_0.7.8_linux_" + arch + ".deb"
		if err := os.WriteFile(filepath.Join(both, name), deb, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(both, name+".sig"), s.sign(t, deb), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.run(t, importOptions{paths: []string{both}, setLatest: true}); err != nil {
		t.Fatal(err)
	}

	h.out.Reset()
	armOnly := writeRelease(t, s, "0.7.9")
	if err := h.run(t, importOptions{paths: []string{armOnly.dir}, setLatest: true}); err != nil {
		t.Fatal(err)
	}
	if out := h.out.String(); !strings.Contains(out, "latest for amd64 still installs 0.7.8") {
		t.Fatalf("summary does not name the architecture left behind:\n%s", out)
	}
}
