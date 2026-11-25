package middleware

import (
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/logging"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// JWTAuth creates JWT authentication middleware
func JWTAuth(cfg config.JWTConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract Bearer token from Authorization header
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				return results.SendUnauthorizedError(c, "missing authorization header")
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				return results.SendUnauthorizedError(c, "invalid authorization header format")
			}

			tokenString := parts[1]

			// Parse and validate JWT. Pin the signing method to HMAC so a token
			// cannot be presented with an unexpected algorithm.
			token, err := jwt.ParseWithClaims(tokenString, &services.TokenClaims{}, func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrTokenSignatureInvalid
				}
				return []byte(cfg.SecretKey), nil
			}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

			if err != nil {
				return results.SendUnauthorizedError(c, "invalid token")
			}

			if !token.Valid {
				return results.SendUnauthorizedError(c, "invalid token")
			}

			claims, ok := token.Claims.(*services.TokenClaims)
			if !ok {
				return results.SendUnauthorizedError(c, "invalid token claims")
			}

			// Check if token is expired
			if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
				return results.SendUnauthorizedError(c, "token expired")
			}

			// Ensure it's an access token (refresh and pre-auth tokens must
			// not authenticate API requests)
			if claims.TokenType != services.TokenTypeAccess {
				return results.SendUnauthorizedError(c, "invalid token type")
			}

			// Set user context. Roles are NOT read from the token — they live
			// in role bindings and are resolved per request.
			c.Set("userID", claims.UserID.String())
			c.Set("accountID", claims.AccountID.String())
			c.Set("email", claims.Email)
			// The session (token family) this request belongs to, so the session
			// list can mark "this device" and "sign out everywhere else" can
			// spare it. Empty on tokens minted before family ids existed.
			c.Set("familyID", claims.FamilyID.String())
			// When the token was minted, so an operation that mints further
			// credentials for this caller can test it against the user's last
			// password change the way a refresh is tested.
			if claims.IssuedAt != nil {
				c.Set("tokenIssuedAt", claims.IssuedAt.Time)
			}
			c.Set("tokenGeneration", claims.CredentialGeneration)

			// Carry the tenant identity in the request CONTEXT as well, not only
			// in the Echo context. This is the first point at which it is known,
			// and it is what puts account_id/user_id on the service log lines
			// that explain what a request did — the Echo context reaches the
			// access log and nothing below the handler. The email is deliberately
			// NOT carried: it is personal data and the ids are what a support
			// question is answered with.
			req := c.Request()
			c.SetRequest(req.WithContext(logging.WithScope(req.Context(), logging.Scope{
				AccountID: claims.AccountID.String(),
				UserID:    claims.UserID.String(),
			})))

			return next(c)
		}
	}
}

// GetUserIDFromContext extracts user ID from Echo context
func GetUserIDFromContext(c echo.Context) (uuid.UUID, error) {
	userIDStr, ok := c.Get("userID").(string)
	if !ok {
		return uuid.Nil, app_errors.ErrUnauthorized
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return uuid.Nil, app_errors.ErrUnauthorized
	}

	return userID, nil
}

// GetAccountIDFromContext extracts account ID from Echo context
func GetAccountIDFromContext(c echo.Context) (uuid.UUID, error) {
	accountIDStr, ok := c.Get("accountID").(string)
	if !ok {
		return uuid.Nil, app_errors.ErrUnauthorized
	}

	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		return uuid.Nil, app_errors.ErrUnauthorized
	}

	return accountID, nil
}

// GetSessionFamilyFromContext returns the token family (session) the request
// authenticated with. uuid.Nil when the caller holds a token minted before
// family ids existed — callers must treat that as "unknown session", never as a
// match, or an old token would look like every session at once.
func GetSessionFamilyFromContext(c echo.Context) uuid.UUID {
	raw, ok := c.Get("familyID").(string)
	if !ok {
		return uuid.Nil
	}
	familyID, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return familyID
}

// GetSessionProofFromContext returns what the presented access token says
// about its session: the family and the mint time. Either may be zero on a
// token minted before the claim existed; the service refuses such a proof
// rather than guessing.
func GetSessionProofFromContext(c echo.Context) services.SessionProof {
	proof := services.SessionProof{FamilyID: GetSessionFamilyFromContext(c)}
	if issuedAt, ok := c.Get("tokenIssuedAt").(time.Time); ok {
		proof.IssuedAt = issuedAt
	}
	if gen, ok := c.Get("tokenGeneration").(int64); ok {
		proof.CredentialGeneration = gen
	}
	return proof
}

// GetEmailFromContext extracts email from Echo context
func GetEmailFromContext(c echo.Context) (string, error) {
	email, ok := c.Get("email").(string)
	if !ok {
		return "", app_errors.ErrUnauthorized
	}

	return email, nil
}
