package user

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// POST /auth/switch-account mints credentials for an already signed-in caller,
// so the service must be told which SESSION asked — the family and mint time
// off the access token — and a refusal must leave the caller with nothing.
// The service-level tests (services/auth/switch_account_test.go) prove the
// gate; this one proves the route feeds it, through the real JWT middleware,
// because a proof the handler forgot to pass is a gate that never runs.

// proofRecordingAuthService captures the proof the handler hands over and
// answers with whatever the test decided.
type proofRecordingAuthService struct {
	services.AuthService
	got    services.SessionProof
	answer error
}

func (s *proofRecordingAuthService) SwitchAccount(_ context.Context, _, _ uuid.UUID, session services.SessionProof) (*services.TokenPair, error) {
	s.got = session
	if s.answer != nil {
		return nil, s.answer
	}
	return &services.TokenPair{AccessToken: "minted-access", RefreshToken: "minted-refresh", TokenType: "Bearer"}, nil
}

func driveSwitchAccount(t *testing.T, svc *proofRecordingAuthService, cfg *config.Config, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	NewAuthHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), svc, cfg).Register(e, middleware.JWTAuth(cfg.JWT))
	req := httptest.NewRequest(http.MethodPost, "/auth/switch-account",
		strings.NewReader(`{"account_id":"`+uuid.New().String()+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func mintAccessToken(t *testing.T, cfg config.JWTConfig, familyID uuid.UUID, issuedAt time.Time) string {
	t.Helper()
	claims := &services.TokenClaims{
		UserID:    uuid.New(),
		AccountID: uuid.New(),
		TokenType: services.TokenTypeAccess,
		FamilyID:  familyID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(cfg.AccessTokenTTL)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.SecretKey))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func switchTestConfig() *config.Config {
	return &config.Config{JWT: config.JWTConfig{SecretKey: "test-secret", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour}}
}

func TestSwitchAccountRouteHandsTheSessionProofToTheService(t *testing.T) {
	cfg := switchTestConfig()
	familyID := uuid.New()
	issuedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	svc := &proofRecordingAuthService{}

	rec := driveSwitchAccount(t, svc, cfg, mintAccessToken(t, cfg.JWT, familyID, issuedAt))

	if rec.Code != http.StatusOK {
		t.Fatalf("live switch answered %d: %s", rec.Code, rec.Body.String())
	}
	if svc.got.FamilyID != familyID {
		t.Fatalf("service received family %s, want the access token's %s", svc.got.FamilyID, familyID)
	}
	if !svc.got.IssuedAt.Equal(issuedAt) {
		t.Fatalf("service received issued-at %s, want the access token's %s", svc.got.IssuedAt, issuedAt)
	}
}

func TestSwitchAccountRouteRefusesWithoutACredential(t *testing.T) {
	cfg := switchTestConfig()
	svc := &proofRecordingAuthService{answer: app_errors.ErrTokenInvalid}

	rec := driveSwitchAccount(t, svc, cfg, mintAccessToken(t, cfg.JWT, uuid.New(), time.Now()))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a dead session answered %d, want 401: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data *struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data != nil && (body.Data.AccessToken != "" || body.Data.RefreshToken != "") {
		t.Fatalf("refusal still carried a credential: %s", rec.Body.String())
	}
	if !strings.Contains(body.Message, "Sign in again") {
		t.Fatalf("refusal must tell the operator to sign in again, got %q", body.Message)
	}
}
