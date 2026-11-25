package main

import (
	"context"
	"log/slog"
	"time"
)

// serialLeaseSweepInterval paces the lease release. Hourly: the lease is
// measured in days, so promptness within an hour is plenty, and the statement
// touches only rows that qualify.
const serialLeaseSweepInterval = time.Hour

// serialLeaseReleaser is the one method the sweep needs from the node service.
type serialLeaseReleaser interface {
	ReleaseUnclaimedSerialLeases(ctx context.Context, unseenSince time.Time) (int, error)
}

// startSerialLeaseSweep releases the serials of never-admitted nodes nothing has
// been seen from for leaseDays, until the context is cancelled. 0 disables it
// and says so once, so a disabled lease is a visible decision in the log rather
// than a silent absence. The first sweep runs immediately: a backlog of stale
// placeholders is cut on deploy, not an hour later. Errors are logged and
// retried next tick, never fatal.
func startSerialLeaseSweep(ctx context.Context, leaseDays int, nodes serialLeaseReleaser, logger *slog.Logger) {
	log := logger.With("service", "serial_lease_sweep")
	if leaseDays <= 0 {
		log.Info("serial lease disabled; a never-admitted node holds its serial forever")
		return
	}
	if nodes == nil {
		return
	}
	lease := time.Duration(leaseDays) * 24 * time.Hour

	go func() {
		sweep := func() {
			released, err := nodes.ReleaseUnclaimedSerialLeases(ctx, time.Now().Add(-lease))
			if err != nil {
				log.Warn("failed to release unclaimed serial leases", "error", err)
			} else if released > 0 {
				log.Info("released unclaimed serial leases", "released", released, "lease_days", leaseDays)
			}
		}
		sweep()

		ticker := time.NewTicker(serialLeaseSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}
