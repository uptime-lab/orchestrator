package local

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tempSweepInterval matches the other retention jobs. Deletion is idempotent,
// so the cadence is not critical.
const tempSweepInterval = time.Hour

// StartTempSweep reaps abandoned "<image-id>.<ext>.<random>.tmp" files under the
// storage base, at startup and hourly thereafter.
//
// StoreWithOptions writes to a unique temp file beside the destination and
// renames at the end, which is what makes a store atomic. The rename never
// happens if the process dies mid-stream — a deploy, a restart, an OOM — and
// what is left is a multi-GB file whose RANDOM name puts it on no row: the
// per-account byte sum misses it, SumBytesAllAccounts misses it, an account
// purge misses it (its keys are derived from rows) and account deletion misses
// it. That is up to IMAGE_MAX_UPLOAD_SIZE per event, on the volume
// that holds every image.
//
// tmpsweep.Sweeper does not cover this: it matches the "multipart-" prefix in
// TMPDIR, which is a different directory and a different naming scheme, and
// SweepStaleCompressionArtifacts names only the two derived compression paths
// for records left in_progress. In-process error paths DO clean up after
// themselves, so only process death reaches this.
//
// A non-positive maxAge disables the sweep and says so: a silent no-op here is
// indistinguishable from a sweep that runs and finds nothing.
func (l *LocalStorageClient) StartTempSweep(ctx context.Context, maxAge time.Duration, logger *slog.Logger) {
	if logger == nil {
		panic("local.StartTempSweep: logger is required")
	}
	logger = logger.With("service", "storage_temp_sweep")
	if maxAge <= 0 {
		logger.Info("storage temp sweeping disabled; a store interrupted by process death leaves its partial file forever",
			"dir", l.baseDir)
		return
	}
	go func() {
		l.SweepTemps(ctx, maxAge, logger)
		ticker := time.NewTicker(tempSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.SweepTemps(ctx, maxAge, logger)
			}
		}
	}()
	logger.Info("storage temp sweeping started",
		"dir", l.baseDir, "max_age", maxAge, "interval", tempSweepInterval)
}

// SweepTemps removes stale partial stores once. Exported so a test can run one
// pass deterministically.
func (l *LocalStorageClient) SweepTemps(ctx context.Context, maxAge time.Duration, logger *slog.Logger) (removed int, reclaimed int64) {
	cutoff := time.Now().Add(-maxAge)

	err := filepath.WalkDir(l.baseDir, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// A directory that vanished under the walk is not worth a warning:
			// account directories come and go with account deletion.
			return nil //nolint:nilerr // keep walking
		}
		if d.IsDir() || !isPartialStore(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // vanished between the walk and the stat
		}
		// Modification time, not creation: a partial store that is still being
		// written has an advancing mtime and can never look stale. That is what
		// makes removing one safe at all — the writer holds the file OPEN and
		// renames it by path, so unlinking a live one would corrupt an upload
		// in flight rather than merely reclaiming space.
		if info.ModTime().After(cutoff) {
			return nil
		}
		size := info.Size()
		if rmErr := os.Remove(path); rmErr != nil {
			if !os.IsNotExist(rmErr) {
				logger.WarnContext(ctx, "failed to remove an abandoned partial store", "error", rmErr, "path", path)
			}
			return nil
		}
		removed++
		reclaimed += size
		logger.InfoContext(ctx, "removed an abandoned partial store",
			"path", path, "size_bytes", size, "age", time.Since(info.ModTime()).Truncate(time.Minute).String())
		return nil
	})
	if err != nil && !os.IsNotExist(err) && ctx.Err() == nil {
		logger.WarnContext(ctx, "storage temp sweep did not complete", "error", err, "dir", l.baseDir)
	}
	if removed > 0 {
		logger.InfoContext(ctx, "storage temp sweep complete", "files_removed", removed, "bytes_reclaimed", reclaimed)
	}
	return removed, reclaimed
}

// isPartialStore matches what StoreWithOptions creates and nothing else.
//
// os.CreateTemp(dir, "<image-id>.<ext>.*.tmp") yields
// "<image-id>.<ext>.<random>.tmp", so the test is a ".tmp" suffix plus at least
// one more dot — deliberately narrow, because a sweeper that removes unfamiliar
// files under the directory holding every stored image would be a far worse
// bug than the leak it was written for.
func isPartialStore(name string) bool {
	if !strings.HasSuffix(name, ".tmp") {
		return false
	}
	return strings.Contains(strings.TrimSuffix(name, ".tmp"), ".")
}
