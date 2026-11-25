// Package tmpsweep reaps orphaned multipart spill files from the temp
// directory.
//
// Go's net/http spills a multipart body larger than its in-memory budget into
// TMPDIR and normally removes it when the response finishes. A spill can
// nevertheless outlive its request — a missed cleanup or an abnormally
// terminated response leaves a multi-GB file nothing holds open — so this
// sweeper is the backstop, and the handler also removes its own spill
// explicitly.
//
// Why a backstop is worth having regardless: TMPDIR is deliberately pointed at
// the storage volume (compose sets TMPDIR=/app/storage/tmp) so that a spill
// cannot fill the root filesystem out from under Postgres. That makes an
// orphan cost real image capacity — a single one can be a large share of the
// free space — and it also lands inside the directory an off-host backup sync
// would mirror, so garbage here is uploaded and then retained remotely as well.
package tmpsweep

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// sweepInterval matches the other retention jobs. Deletion is idempotent,
	// so the cadence is not critical.
	sweepInterval = time.Hour

	// spillPrefix is what mime/multipart names its temp files
	// (os.CreateTemp(dir, "multipart-")). The sweeper matches on it rather than
	// deleting whatever it finds: TMPDIR is shared with anything else in the
	// process that wants scratch space, and a sweeper that removes unfamiliar
	// files is a much worse bug than the leak it was written for.
	spillPrefix = "multipart-"
)

// Sweeper deletes multipart spill files whose modification time is older than
// MaxAge from Dir.
type Sweeper struct {
	dir    string
	maxAge time.Duration
	logger *slog.Logger
}

// New builds a Sweeper. dir is normally os.TempDir().
func New(dir string, maxAge time.Duration, logger *slog.Logger) *Sweeper {
	if logger == nil {
		panic("tmpsweep.New: logger is required")
	}
	return &Sweeper{
		dir:    dir,
		maxAge: maxAge,
		logger: logger.With("service", "tmpsweep"),
	}
}

// Start runs the sweep hourly until ctx is cancelled. A non-positive maxAge
// disables it entirely, and says so — a silent no-op here would be
// indistinguishable from a sweeper that runs and finds nothing.
func (s *Sweeper) Start(ctx context.Context) {
	if s.maxAge <= 0 {
		s.logger.Info("multipart spill sweeping disabled; abandoned uploads accumulate in the temp directory",
			"dir", s.dir)
		return
	}
	go func() {
		s.Sweep(ctx)
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Sweep(ctx)
			}
		}
	}()
	s.logger.Info("multipart spill sweeping started",
		"dir", s.dir, "max_age", s.maxAge, "interval", sweepInterval)
}

// Sweep removes stale spill files once. Exported so a test — and, if it ever
// matters, an operator endpoint — can run one pass deterministically.
func (s *Sweeper) Sweep(ctx context.Context) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		// Not fatal and not even a warning-per-hour: a temp directory that does
		// not exist yet is normal on a fresh install, and it is created lazily
		// by the first upload.
		if !os.IsNotExist(err) {
			s.logger.WarnContext(ctx, "failed to read temp directory", "error", err, "dir", s.dir)
		}
		return
	}

	cutoff := time.Now().Add(-s.maxAge)
	var removed, bytes int64
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || !strings.HasPrefix(e.Name(), spillPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // vanished between ReadDir and Info; nothing to do
		}
		// Modification time, not creation: a spill that is still being written
		// by an in-flight upload has a advancing mtime and can never look
		// stale. That is what makes deleting one safe at all — the temp file is
		// reopened BY PATH after the body is received (multipart.FileHeader.Open),
		// so unlinking a live one would fail the upload rather than merely
		// losing scratch space.
		if info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		size := info.Size()
		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) {
				s.logger.WarnContext(ctx, "failed to remove stale multipart spill",
					"error", err, "path", path)
			}
			continue
		}
		removed++
		bytes += size
		s.logger.InfoContext(ctx, "removed stale multipart spill",
			"path", path, "size_bytes", size, "age", time.Since(info.ModTime()).Truncate(time.Minute).String())
	}
	if removed > 0 {
		s.logger.InfoContext(ctx, "multipart spill sweep complete",
			"files_removed", removed, "bytes_reclaimed", bytes)
	}
}
