package local

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSweepClient(t *testing.T) *LocalStorageClient {
	t.Helper()
	c, err := NewLocalStorageClient(t.TempDir(), "http://example.invalid")
	if err != nil {
		t.Fatalf("NewLocalStorageClient: %v", err)
	}
	return c
}

func writeAged(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// A store interrupted by process death leaves "<image-id>.<ext>.<random>.tmp"
// beside its destination, under a random name that appears on no row: the
// per-account byte sum misses it, SumBytesAllAccounts misses it, an account purge
// misses it (keys are derived from rows) and account deletion misses it. That
// is up to IMAGE_MAX_UPLOAD_SIZE per event on the volume that holds every
// image.
func TestSweepRemovesAnAbandonedPartialStore(t *testing.T) {
	c := newSweepClient(t)
	orphan := filepath.Join(c.baseDir, "acct-1", "img-1.img.129384.tmp")
	writeAged(t, orphan, 2048, 4*time.Hour)

	removed, reclaimed := c.SweepTemps(context.Background(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if removed != 1 || reclaimed != 2048 {
		t.Fatalf("removed=%d reclaimed=%d, want 1/2048", removed, reclaimed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("the abandoned partial store is still there: %v", err)
	}
}

// A store still streaming has an advancing mtime, and unlinking it would corrupt
// an upload in flight rather than reclaim anything — the writer holds it open and
// renames it BY PATH.
func TestSweepLeavesAFreshPartialStoreAlone(t *testing.T) {
	c := newSweepClient(t)
	live := filepath.Join(c.baseDir, "acct-1", "img-2.img.4711.tmp")
	writeAged(t, live, 512, time.Minute)

	if removed, _ := c.SweepTemps(context.Background(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil))); removed != 0 {
		t.Fatalf("removed=%d, want 0: an in-flight store must survive", removed)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("an in-flight store was deleted: %v", err)
	}
}

// The match is deliberately narrow. This sweeper walks the directory holding
// every stored image, so removing an unfamiliar file would be a far worse bug
// than the leak it was written for.
func TestSweepTouchesNothingItDoesNotRecognise(t *testing.T) {
	c := newSweepClient(t)
	keep := []string{
		filepath.Join(c.baseDir, "acct-1", "img-3.img"),      // a real image
		filepath.Join(c.baseDir, "acct-1", "img-3.img.meta"), // its metadata
		filepath.Join(c.baseDir, "acct-1", "notes.tmp"),      // no second dot: not ours
		filepath.Join(c.baseDir, "multipart-9999"),           // the other sweeper's business
	}
	for _, p := range keep {
		writeAged(t, p, 16, 48*time.Hour)
	}

	if removed, _ := c.SweepTemps(context.Background(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil))); removed != 0 {
		t.Fatalf("removed=%d, want 0", removed)
	}
	for _, p := range keep {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted: %v", filepath.Base(p), err)
		}
	}
}

// The name the sweeper matches must be the name the store actually writes. A
// pattern copied by hand into the sweeper is exactly the thing that rots when
// StoreWithOptions changes, so derive it from a real interrupted store.
func TestTheSweptNameIsTheNameAStoreWrites(t *testing.T) {
	c := newSweepClient(t)
	dir := filepath.Join(c.baseDir, "acct-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Exactly what StoreWithOptions creates before it renames.
	f, err := os.CreateTemp(dir, filepath.Base("img-4.img")+".*.tmp")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	name := filepath.Base(f.Name())
	f.Close()

	if !isPartialStore(name) {
		t.Fatalf("the sweeper does not recognise %q, which is what a store leaves behind", name)
	}
	if !strings.HasSuffix(name, ".tmp") {
		t.Fatalf("unexpected temp name %q", name)
	}
}
