package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

const jwtTestSecret = "unit-test-secret-key-not-a-real-credential"

// mintToken signs a TokenClaims the same way the auth service does (HS256).
// mutate lets each case break exactly one property of an otherwise valid
// access token, so a 401 in the test is attributable to that property alone.
func mintToken(t *testing.T, secret string, mutate func(*services.TokenClaims)) string {
	t.Helper()
	now := time.Now()
	claims := &services.TokenClaims{
		UserID:    uuid.New(),
		AccountID: uuid.New(),
		Email:     "user@example.com",
		TokenType: services.TokenTypeAccess,
		FamilyID:  uuid.New(),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "orchestrator-test",
			Subject:   "test",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	if mutate != nil {
		mutate(claims)
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("signing test token: %v", err)
	}
	return signed
}

// driveJWT runs one request through JWTAuth and reports the response status
// plus whether the wrapped handler was reached.
func driveJWT(t *testing.T, authHeader string) (status int, handlerRan bool, ctx echo.Context) {
	status, handlerRan, ctx, _ = driveJWTWithBody(t, authHeader)
	return status, handlerRan, ctx
}

func driveJWTWithBody(t *testing.T, authHeader string) (status int, handlerRan bool, ctx echo.Context, body string) {
	t.Helper()
	e := echo.New()
	h := JWTAuth(config.JWTConfig{SecretKey: jwtTestSecret})(func(c echo.Context) error {
		handlerRan = true
		return c.NoContent(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h(c); err != nil {
		t.Fatalf("middleware returned an error instead of writing a response: %v", err)
	}
	return rec.Code, handlerRan, c, rec.Body.String()
}

func TestJWTAuthRejectsBadTokens(t *testing.T) {
	cases := []struct {
		name       string
		authHeader string
	}{
		{"missing header", ""},
		{"not a bearer scheme", "Basic dXNlcjpwYXNz"},
		{"bearer with no token", "Bearer"},
		{"garbage token", "Bearer not.a.jwt"},
		{"wrong signature", "Bearer " + mintToken(t, "a-different-secret-entirely-32-chars", nil)},
		{"expired token", "Bearer " + mintToken(t, jwtTestSecret, func(c *services.TokenClaims) {
			c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
		})},
		// Only the access token class authenticates API requests. A refresh
		// token can mint new tokens and a pre_auth token predates account
		// selection — neither must pass as an authenticated request.
		{"refresh token used as access", "Bearer " + mintToken(t, jwtTestSecret, func(c *services.TokenClaims) {
			c.TokenType = services.TokenTypeRefresh
		})},
		{"pre_auth token used as access", "Bearer " + mintToken(t, jwtTestSecret, func(c *services.TokenClaims) {
			c.TokenType = services.TokenTypePreAuth
			c.AccountID = uuid.Nil
		})},
		{"two_factor ticket used as access", "Bearer " + mintToken(t, jwtTestSecret, func(c *services.TokenClaims) {
			c.TokenType = services.TokenTypeTwoFactor
		})},
		{"empty token type", "Bearer " + mintToken(t, jwtTestSecret, func(c *services.TokenClaims) {
			c.TokenType = ""
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, handlerRan, _, body := driveJWTWithBody(t, tc.authHeader)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", status)
			}
			if handlerRan {
				t.Error("the wrapped handler ran despite the rejected token")
			}
			// The 401 is the results envelope like every other refusal, not
			// the bare {"error": …} map it used to be: the frontend's refresh
			// flow keys on the status alone, and the message is what a script
			// reads.
			var env struct {
				Success   bool   `json:"success"`
				Message   string `json:"message"`
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal([]byte(body), &env); err != nil {
				t.Fatalf("401 body is not JSON: %v — %s", err, body)
			}
			if env.Success || env.Message == "" || env.Timestamp == "" {
				t.Errorf("401 body is not the results envelope: %s", body)
			}
		})
	}
}

// The unsigned "none" algorithm must be refused: the middleware pins HMAC via
// WithValidMethods, and this asserts that pin actually holds.
func TestJWTAuthRejectsUnsignedAlgorithm(t *testing.T) {
	claims := &services.TokenClaims{
		UserID:    uuid.New(),
		AccountID: uuid.New(),
		TokenType: services.TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("building unsigned token: %v", err)
	}
	status, handlerRan, _ := driveJWT(t, "Bearer "+unsigned)
	if status != http.StatusUnauthorized || handlerRan {
		t.Errorf("unsigned token: status = %d, handlerRan = %v; want 401 and no handler", status, handlerRan)
	}
}

func TestJWTAuthAcceptsAValidAccessTokenAndSetsTheContext(t *testing.T) {
	userID, accountID, familyID := uuid.New(), uuid.New(), uuid.New()
	token := mintToken(t, jwtTestSecret, func(c *services.TokenClaims) {
		c.UserID = userID
		c.AccountID = accountID
		c.Email = "owner@example.com"
		c.FamilyID = familyID
	})

	status, handlerRan, c := driveJWT(t, "Bearer "+token)
	if status != http.StatusOK || !handlerRan {
		t.Fatalf("status = %d, handlerRan = %v; want 200 and the handler reached", status, handlerRan)
	}

	// The middleware's contract with everything below it: the tenant identity
	// is on the Echo context as strings.
	if got, _ := GetUserIDFromContext(c); got != userID {
		t.Errorf("userID in context = %v, want %v", got, userID)
	}
	if got, _ := GetAccountIDFromContext(c); got != accountID {
		t.Errorf("accountID in context = %v, want %v", got, accountID)
	}
	if got, _ := GetEmailFromContext(c); got != "owner@example.com" {
		t.Errorf("email in context = %q, want %q", got, "owner@example.com")
	}
	if got := GetSessionFamilyFromContext(c); got != familyID {
		t.Errorf("familyID in context = %v, want %v", got, familyID)
	}
}
