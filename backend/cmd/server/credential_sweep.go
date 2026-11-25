package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
)

// credentialSweepInterval paces the expired-credential prune. Daily is enough:
// both tables' rows expire on the scale of hours to days, the deletes are
// idempotent, and the point is bounding growth, not promptness.
const credentialSweepInterval = 24 * time.Hour

// startExpiredCredentialSweep prunes expired refresh tokens and email
// verifications until the context is cancelled.
//
// Both tables had DeleteExpired implementations with no periodic caller — the
// only sweeps were opportunistic (password resets on request, adoption codes on
// mint), so refresh_tokens and email_verifications grew forever. The first
// sweep runs immediately so a long-lived backlog is cut on deploy, not a day
// later. Errors are logged and retried next tick, never fatal: a prune that
// takes the backend down has inverted its purpose.
func startExpiredCredentialSweep(
	ctx context.Context,
	refreshTokens repository.RefreshTokenRepository,
	emailVerifications repository.EmailVerificationRepository,
	logger *slog.Logger,
) {
	if refreshTokens == nil || emailVerifications == nil {
		return
	}
	log := logger.With("service", "credential_sweep")

	go func() {
		sweep := func() {
			if deleted, err := refreshTokens.DeleteExpired(ctx); err != nil {
				log.Warn("failed to sweep expired refresh tokens", "error", err)
			} else if deleted > 0 {
				log.Info("swept expired refresh tokens", "deleted", deleted)
			}
			if deleted, err := emailVerifications.DeleteExpired(ctx); err != nil {
				log.Warn("failed to sweep expired email verifications", "error", err)
			} else if deleted > 0 {
				log.Info("swept expired email verifications", "deleted", deleted)
			}
		}
		sweep()

		ticker := time.NewTicker(credentialSweepInterval)
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
