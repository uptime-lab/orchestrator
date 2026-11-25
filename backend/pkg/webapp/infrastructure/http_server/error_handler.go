package http_server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
)

// envelopeErrorHandler renders every error a handler or middleware RETURNS —
// as opposed to writes — in the same results.APIResponse envelope the
// results.Send* helpers emit, so the API has one error shape.
//
// The returned-error path is not a leftover: middleware.Authorize and the
// per-handler authorize helpers deliberately return a *echo.HTTPError instead
// of writing, so a caller cannot continue past a denial (see the comment on
// Authorize), and one caller inspects the returned Code to turn a 403 into a
// 404. Rewriting those sites to the Send* helpers would break that contract;
// rendering them here does not. Echo's own errors (route not found, method not
// allowed, the global body limit) and a panic recovered by middleware.Recover
// arrive here too.
//
// What is preserved from Echo's default handler: the status code, the message
// text, nothing written on an already-committed response, no body on HEAD,
// and — for a plain error that is not an *echo.HTTPError — a generic 500
// whose body carries NOTHING of the error, since it can hold SQL text, table
// names or filesystem paths.
func envelopeErrorHandler(logger *slog.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		he, ok := err.(*echo.HTTPError)
		if ok {
			if he.Internal != nil {
				if herr, ok := he.Internal.(*echo.HTTPError); ok {
					he = herr
				}
			}
		} else {
			he = &echo.HTTPError{
				Code:    http.StatusInternalServerError,
				Message: http.StatusText(http.StatusInternalServerError),
			}
		}

		if c.Request().Method == http.MethodHead {
			if nerr := c.NoContent(he.Code); nerr != nil {
				logger.Error("failed to write error response", "error", nerr)
			}
			return
		}

		var sendErr error
		switch m := he.Message.(type) {
		case string:
			sendErr = results.SendError(c, he.Code, m, nil)
		case error:
			sendErr = results.SendError(c, he.Code, m.Error(), nil)
		case json.Marshaler:
			// A message that formats itself is sent as itself, as Echo does.
			sendErr = c.JSON(he.Code, m)
		default:
			sendErr = c.JSON(he.Code, m)
		}
		if sendErr != nil {
			logger.Error("failed to write error response", "error", sendErr)
		}
	}
}
