package middleware

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/logging"
	"github.com/labstack/echo/v4"
)

// RequestScope puts the request id into the request CONTEXT, so every log line a
// handler or service writes with a context-aware slog call carries it.
//
// `middleware.RequestID` only sets a header and an Echo-context value, which the
// access log reads and nothing else can: a service is handed a
// `context.Context`, not an `echo.Context`. So the id existed on exactly one line
// per request and the twenty service lines that explain what actually happened
// were unattributable.
//
// Installed globally and therefore BEFORE authentication, so an unauthenticated
// request (a failed login, a netboot fetch, an adoption exchange) still
// correlates. The tenant identity is added later by the JWT middleware, which is
// the first point at which it is known — logging.WithScope merges rather than
// replaces for exactly that reason.
func RequestScope() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			requestID := c.Response().Header().Get(echo.HeaderXRequestID)
			if requestID == "" {
				requestID = c.Request().Header.Get(echo.HeaderXRequestID)
			}
			if requestID == "" {
				return next(c)
			}
			req := c.Request()
			c.SetRequest(req.WithContext(
				logging.WithScope(req.Context(), logging.Scope{RequestID: requestID}),
			))
			return next(c)
		}
	}
}
