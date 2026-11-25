package tmpsweep

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// write creates a file with the given name and back-dates it by age.
func write(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return path
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepRemovesOnlyStaleSpills(t *testing.T) {
	dir := t.TempDir()
	stale := write(t, dir, "multipart-3322859059", 8*time.Hour)
	fresh := write(t, dir, "multipart-1111111111", 10*time.Minute)

	New(dir, 6*time.Hour, testLogger()).Sweep(context.Background())

	if exists(t, stale) {
		t.Error("stale spill survived the sweep")
	}
	if !exists(t, fresh) {
		t.Fatal("a spill younger than max age was deleted — an upload in flight writes continuously, " +
			"so a fresh mtime is exactly what a live upload looks like")
	}
}

// The sweeper shares TMPDIR with anything else that wants scratch space.
// Deleting an unfamiliar file would be a worse bug than the leak it exists for.
func TestSweepIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	foreign := write(t, dir, "some-other-tool.dat", 30*24*time.Hour)
	subdir := filepath.Join(dir, "multipart-shaped-directory")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	New(dir, time.Hour, testLogger()).Sweep(context.Background())

	if !exists(t, foreign) {
		t.Error("deleted a file it does not own")
	}
	if !exists(t, subdir) {
		t.Error("deleted a directory whose name merely matched the spill prefix")
	}
}

func TestSweepIsDisabledByNonPositiveMaxAge(t *testing.T) {
	dir := t.TempDir()
	stale := write(t, dir, "multipart-42", 30*24*time.Hour)

	s := New(dir, 0, testLogger())
	s.Start(context.Background())
	// Start must return without spawning the loop; nothing should be touched.
	if !exists(t, stale) {
		t.Error("swept with sweeping disabled")
	}
}

func TestSweepToleratesMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created-yet")
	// A fresh install has no temp directory until the first upload spills.
	// This must not panic and must not log an error every hour.
	New(missing, time.Hour, testLogger()).Sweep(context.Background())
}

func TestStartSweepsImmediatelyAndStopsWithContext(t *testing.T) {
	dir := t.TempDir()
	stale := write(t, dir, "multipart-7", 48*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	New(dir, time.Hour, testLogger()).Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !exists(t, stale) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Start did not sweep before its first tick; an hourly job that waits an hour " +
		"to do its first pass leaves a full disk full for an hour after a restart")
}
