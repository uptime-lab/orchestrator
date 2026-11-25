package user

import (
	"errors"
	"net/http"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Session inventory for the signed-in user. Everything here is
// scoped to the caller's own user id — these are not administrative endpoints,
// and the family id in the URL is something the client was handed, so the
// ownership check lives in the repository query rather than in this layer.

// SessionResult is one live login as the client sees it.
type SessionResult struct {
	ID         string `json:"id"`
	UserAgent  string `json:"user_agent,omitempty"`
	IP         string `json:"ip,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	ExpiresAt  string `json:"expires_at"`
	// Current marks the session this very request came in on.
	Current bool `json:"current"`
}

func sessionResults(sessions []refresh_token.Session) []SessionResult {
	out := make([]SessionResult, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, SessionResult{
			ID:         s.FamilyID.String(),
			UserAgent:  s.UserAgent,
			IP:         s.IP,
			CreatedAt:  s.CreatedAt.Format(timeFormat),
			LastUsedAt: s.LastUsedAt.Format(timeFormat),
			ExpiresAt:  s.ExpiresAt.Format(timeFormat),
			Current:    s.Current,
		})
	}
	return out
}

const timeFormat = "2006-01-02T15:04:05Z07:00"

// ListSessions returns the caller's live sessions.
// @Summary List my sessions
// @Description Every live login for the signed-in user — one entry per session, however many times its token has rotated — newest activity first. The entry for the requesting session is marked `current`.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]SessionResult} "Live sessions"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /auth/sessions [get]
func (h *AuthHandler) ListSessions(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	sessions, err := h.authService.ListSessions(c.Request().Context(), userID,
		middleware.GetSessionFamilyFromContext(c))
	if err != nil {
		h.logger.Error("failed to list sessions", "error", err, "user_id", userID)
		return results.SendInternalServerError(c, "failed to list sessions", err)
	}
	return results.SendOKResponse(c, sessionResults(sessions), "sessions")
}

// RevokeSession signs one session out.
// @Summary Revoke one of my sessions
// @Description Revokes a single session by id, including every token rotated out of it. Revoking the current session is allowed — it is how "sign out of this browser from another device" works.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Session ID"
// @Success 204 "No Content"
// @Failure 404 {object} results.APIResponse "No such live session"
// @Router /auth/sessions/{id} [delete]
func (h *AuthHandler) RevokeSession(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	familyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid session id")
	}
	if err := h.authService.RevokeSessionFamily(c.Request().Context(), userID, familyID); err != nil {
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			// Already gone, expired, or never theirs — one answer for all
			// three, so the endpoint cannot be used to probe for other
			// people's session ids.
			return results.SendError(c, http.StatusNotFound, "no such live session", nil)
		}
		h.logger.Error("failed to revoke session", "error", err, "user_id", userID)
		return results.SendInternalServerError(c, "failed to revoke the session", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// RevokeOtherSessions signs the user out everywhere but here.
// @Summary Sign out of my other sessions
// @Description Revokes every session except the one making the request. The "I lost a device" action; the caller stays signed in on purpose.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]bool} "Other sessions revoked"
// @Router /auth/sessions/revoke-others [post]
func (h *AuthHandler) RevokeOtherSessions(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	if err := h.authService.RevokeOtherSessions(c.Request().Context(), userID,
		middleware.GetSessionFamilyFromContext(c)); err != nil {
		h.logger.Error("failed to revoke other sessions", "error", err, "user_id", userID)
		return results.SendInternalServerError(c, "failed to revoke the other sessions", err)
	}
	return results.SendOKResponse(c, map[string]bool{"revoked": true}, "other sessions revoked")
}

// deviceOf reads the request-side session identification. Both values are
// self-reported by the client and are only ever shown back to the user who owns
// the session — never used to authorize anything.
func deviceOf(c echo.Context) refresh_token.Device {
	return refresh_token.NewDevice(c.Request().UserAgent(), c.RealIP())
}
