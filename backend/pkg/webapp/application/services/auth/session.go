package auth

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/google/uuid"
)

// Session inventory and the per-user concurrent-session cap.
//
// Rotation and replay-family revocation alone bound nothing and expose nothing:
// a single account can accumulate dozens of unrevoked, unexpired refresh
// tokens, invisible to that user and to the operator. A session here is a
// token FAMILY — one login, however many
// rotations — because that is the unit a person recognises and the unit
// revocation has always operated on.

// SetSessionCap bounds how many concurrent sessions one user may hold. 0 (the
// default) is unlimited, matching the polarity of the other quota knobs.
func (s *AuthService) SetSessionCap(max int) {
	s.maxSessions = max
}

// SetAnomalyRecorder wires the operator-facing anomaly feed. Optional: an
// install with no operator console has nothing reading it, so a nil recorder is a supported configuration.
func (s *AuthService) SetAnomalyRecorder(repo repository.InstanceAnomalyRepository) {
	s.anomalyRepo = repo
}

// ListSessions returns the user's live logins, newest activity first, marking
// the one that made the request.
func (s *AuthService) ListSessions(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]refresh_token.Session, error) {
	sessions, err := s.refreshTokens.ListSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		sessions[i].Current = currentFamilyID != uuid.Nil && sessions[i].FamilyID == currentFamilyID
	}
	return sessions, nil
}

// RevokeSessionFamily signs one session out. Scoped to the owner in the
// repository, because the family id is something the client was handed.
func (s *AuthService) RevokeSessionFamily(ctx context.Context, userID, familyID uuid.UUID) error {
	return s.refreshTokens.RevokeFamilyForUser(ctx, userID, familyID)
}

// RevokeOtherSessions signs the user out everywhere except where they are.
// Keeping the current session is the point: this is the "I lost my phone"
// button, and logging the user out of the browser they are pressing it in makes
// them less likely to press it.
//
// A login still in progress is never the caller's own session, so the pending
// two-factor and account-selection challenges go too.
func (s *AuthService) RevokeOtherSessions(ctx context.Context, userID, keepFamilyID uuid.UUID) error {
	s.revokePendingLogins(userID)
	if keepFamilyID == uuid.Nil {
		// A caller on a token minted before family ids existed cannot name its
		// own session; revoking everything would sign it out too. Refuse rather
		// than surprise it — the token expires within the hour anyway.
		return s.refreshTokens.RevokeAllForUser(ctx, userID)
	}
	return s.refreshTokens.RevokeOtherFamilies(ctx, userID, keepFamilyID)
}

// RecordSessionDevice attaches the request's User-Agent and IP to a freshly
// issued refresh token.
//
// It is a separate call rather than a parameter on token generation because the
// device is known only to the HTTP layer, and the token pair is minted from six
// different login paths. Best-effort: a session that misses its label is
// cosmetically poorer, never broken.
func (s *AuthService) RecordSessionDevice(ctx context.Context, refreshToken string, device refresh_token.Device) {
	if refreshToken == "" || (device.UserAgent == "" && device.IP == "") {
		return
	}
	if err := s.refreshTokens.AnnotateDevice(ctx, refresh_token.Hash(refreshToken), device); err != nil {
		s.logger.WarnContext(ctx, "failed to record session device", "error", err)
	}
}

// enforceSessionCap trims the least recently active sessions past the cap.
//
// Evicting rather than refusing the login is deliberate: a cap that blocks
// sign-in turns a forgotten session into a lockout.
func (s *AuthService) enforceSessionCap(ctx context.Context, userID uuid.UUID) {
	if s.maxSessions <= 0 {
		return
	}
	evicted, err := s.refreshTokens.EnforceSessionCap(ctx, userID, s.maxSessions)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to enforce the session cap", "error", err, "user_id", userID)
		return
	}
	if evicted == 0 {
		return
	}
	s.logger.InfoContext(ctx, "revoked sessions over the per-user cap",
		"user_id", userID, "evicted", evicted, "cap", s.maxSessions)

	// One eviction is what a user at the cap produces every time they log in.
	// More than one in a single login means several sessions appeared since the
	// last one — the growth pattern worth an operator alert, and what a
	// harvested credential being used from several places looks like.
	if evicted > 1 && s.anomalyRepo != nil {
		anomaly := instance_anomaly.New(
			instance_anomaly.KindSessionGrowth,
			instance_anomaly.SessionGrowthKey(userID),
			instance_anomaly.SeverityWarning,
			"a login evicted several sessions at once, so sessions are being created faster than they are used",
			map[string]interface{}{
				"user_id": userID.String(),
				"evicted": evicted,
				"cap":     s.maxSessions,
			},
			nil,
		)
		if err := s.anomalyRepo.Record(ctx, anomaly); err != nil {
			s.logger.WarnContext(ctx, "failed to record session-growth anomaly", "error", err)
		}
	}
}
