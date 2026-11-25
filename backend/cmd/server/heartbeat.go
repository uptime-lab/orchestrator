package main

import (
	"context"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
)

// heartbeatInterval paces the liveness row. A minute matches the console's
// sampling interval, so a restart cannot slip between two samples unnoticed
// while still costing one tiny upsert per minute.
const heartbeatInterval = time.Minute

// startHeartbeat records that this process is alive, every minute, until the
// context is cancelled.
//
// The first beat is written immediately and synchronously in the goroutine, not
// on the first tick: a process that crashes forty seconds in would otherwise
// never write a row at all, and a restart loop tight enough to matter is
// precisely the one that never survives to its first tick.
func startHeartbeat(ctx context.Context, repo repository.InstanceHeartbeatRepository, logger *slog.Logger) {
	if repo == nil {
		return
	}
	startedAt := time.Now()
	version := buildRevision()
	log := logger.With("service", "heartbeat")

	go func() {
		beat := func() {
			// Its own timeout: this is bookkeeping, and it must not be the thing
			// that holds a shutdown open.
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := repo.Beat(writeCtx, repository.ComponentBackend, startedAt, version, 0); err != nil {
				// Warn, never fatal. A backend that refuses to run because it
				// cannot tell the console it is running has inverted the point.
				log.Warn("failed to record backend heartbeat", "error", err)
			}
		}
		beat()

		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				beat()
			}
		}
	}()
}

// stampedRevision is set at link time by the image build
// (`-ldflags "-X main.stampedRevision=$GIT_REVISION"`, backend/Dockerfile),
// the same build argument that sets the image's
// org.opencontainers.image.revision label. The image is built from
// `git archive`, which carries no .git, so the VCS stamp below is empty there;
// without this the heartbeat and the schema-version ledger would record no
// revision for any deployed build.
var stampedRevision string

// stampedVersion is the platform release the image was built for, set at
// link time from the PLATFORM_VERSION build argument (backend/Dockerfile;
// the release workflow passes the tag's version). Empty for a build from
// source, which reports no version.
var stampedVersion string

// buildVersion reports the stamped platform version, trimmed; "" when the
// build stamped none.
func buildVersion() string {
	v := strings.TrimSpace(stampedVersion)
	if len(v) > maxRevisionLen {
		return v[:maxRevisionLen]
	}
	return v
}

// maxRevisionLen bounds what is stored: both the heartbeat and the ledger
// column are 64 characters, and a full commit hash is 40.
const maxRevisionLen = 64

// buildRevision reports the commit this binary was built from, when the build
// recorded one: the link-time stamp first, then the Go toolchain's VCS stamp
// (a `go build` inside a git checkout). Neither is present for a build from a
// copied tree without the stamp, so this can be empty — an empty string is
// honest and the console renders it as unknown rather than inventing a
// version. The Dockerfile's default build argument "unknown" counts as empty.
func buildRevision() string {
	if r := stampedRevision; r != "" && r != "unknown" {
		if len(r) > maxRevisionLen {
			return r[:maxRevisionLen]
		}
		return r
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}
			return setting.Value
		}
	}
	return ""
}
