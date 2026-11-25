package user

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
)

// levelRecorder keeps the level of every record logged with a given message.
type levelRecorder struct {
	mu     sync.Mutex
	levels map[string][]slog.Level
}

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels[rec.Message] = append(r.levels[rec.Message], rec.Level)
	return nil
}
func (r *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *levelRecorder) WithGroup(string) slog.Handler      { return r }

type refusingRefresh struct {
	services.AuthService
	err error
}

func (a *refusingRefresh) RefreshTokenFromDevice(string, refresh_token.Device) (*services.TokenPair, error) {
	return nil, a.err
}

// A refresh token the server refuses — expired, revoked, from a signed-out
// session — is an ordinary event every idle browser produces, and is logged
// at WARN like a wrong password. ERROR stays for the server failing.
func TestRefusedRefreshIsLoggedAtWarnAndAFailureAtError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		level  slog.Level
	}{
		{"invalid token", app_errors.ErrTokenInvalid, http.StatusUnauthorized, slog.LevelWarn},
		{"expired token", app_errors.ErrTokenExpired, http.StatusUnauthorized, slog.LevelWarn},
		{"not a member", app_errors.ErrNotAMember, http.StatusUnauthorized, slog.LevelWarn},
		{"server failure", errors.New("database is down"), http.StatusInternalServerError, slog.LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &levelRecorder{levels: map[string][]slog.Level{}}
			h := NewAuthHandler(slog.New(rec), &refusingRefresh{err: tc.err}, nil)

			resp, _ := call(t, h.RefreshToken, "/auth/refresh", `{"refresh_token":"some-token"}`)

			if resp.Code != tc.status {
				t.Fatalf("status %d, want %d (body %s)", resp.Code, tc.status, resp.Body.String())
			}
			got := rec.levels["token refresh failed"]
			if len(got) != 1 || got[0] != tc.level {
				t.Fatalf("logged %v, want one record at %v", got, tc.level)
			}
		})
	}
}
