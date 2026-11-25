package results

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/labstack/echo/v4"
)

// APIResponse represents a standard API response structure
type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	// Code is a stable machine-readable discriminator for the cases a client
	// must ACT on differently rather than merely display — where the HTTP status
	// alone is ambiguous. Empty on everything that only needs to be shown.
	Code      string    `json:"code,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// NewSuccessResponse creates a successful API response
func NewSuccessResponse(data interface{}, message string) *APIResponse {
	return &APIResponse{
		Success:   true,
		Message:   message,
		Data:      data,
		Timestamp: time.Now(),
	}
}

// NewErrorResponse creates an error API response
func NewErrorResponse(message string, err error) *APIResponse {
	response := &APIResponse{
		Success:   false,
		Message:   message,
		Timestamp: time.Now(),
	}

	if err != nil {
		response.Error = err.Error()
	}

	return response
}

// SendSuccess sends a successful response
func SendSuccess(c echo.Context, statusCode int, data interface{}, message string) error {
	response := NewSuccessResponse(data, message)
	return c.JSON(statusCode, response)
}

// SendError sends an error response
func SendError(c echo.Context, statusCode int, message string, err error) error {
	response := NewErrorResponse(message, err)
	return c.JSON(statusCode, response)
}

// SendErrorWithCode sends an error carrying a machine-readable code, for the
// refusals a client must branch on instead of merely rendering.
func SendErrorWithCode(c echo.Context, statusCode int, message, code string) error {
	response := NewErrorResponse(message, nil)
	response.Code = code
	return c.JSON(statusCode, response)
}

// SendValidationError sends a validation error response
func SendValidationError(c echo.Context, message string) error {
	return SendError(c, http.StatusBadRequest, message, nil)
}

// SendUnauthorizedError sends an unauthorized error response
func SendUnauthorizedError(c echo.Context, message string) error {
	return SendError(c, http.StatusUnauthorized, message, nil)
}

// SendForbiddenError sends a forbidden error response
func SendForbiddenError(c echo.Context, message string) error {
	return SendError(c, http.StatusForbidden, message, nil)
}

// SendNotFoundError sends a not found error response
func SendNotFoundError(c echo.Context, message string) error {
	return SendError(c, http.StatusNotFound, message, nil)
}

// SendInternalServerError sends an internal server error response. The wrapped
// err is intentionally NOT echoed to the client — it can carry SQL/driver text,
// table/column names and filesystem paths. Callers should log err themselves.
//
// The ONE exception is an error that implements OperatorFacingError: it has
// declared its own status, code and an operator-safe message, so it is answered
// as itself rather than as a 500. This is what stops a typed refusal — "this
// node runs the watcher agent", "this agent is too old" — from reaching the
// user as "the platform broke" at whichever handler forgot an arm for it.
func SendInternalServerError(c echo.Context, message string, err error) error {
	var facing OperatorFacingError
	if err != nil && errors.As(err, &facing) && !isNilPointer(facing) {
		// A status outside 4xx/5xx is a bug in the error type, not something to
		// pass to c.JSON: 0 would render as 200 on a recorder and panics on a
		// real ResponseWriter, and a 2xx would report a refusal as success.
		// Fall back rather than trust it.
		if status := facing.HTTPStatus(); status >= 400 && status <= 599 {
			response := NewErrorResponse(facing.Error(), nil)
			response.Code = facing.ErrorCode()
			// The structured values, when the type offers them. Matched on
			// the same error the message came from, not on the wrapping
			// chain, so the details always belong to the message.
			if detailed, ok := facing.(OperatorFacingDetails); ok {
				response.Data = detailed.ErrorDetails()
			}
			return c.JSON(status, response)
		}
	}
	return SendError(c, http.StatusInternalServerError, message, nil)
}

// isNilPointer reports a typed-nil inside a non-nil interface — `(*T)(nil)`
// stored in an error. `errors.As` matches it happily and `facing != nil` is
// TRUE, so calling Error() on a type with a pointer receiver panics. Handlers
// run under middleware.Recover, so the symptom would be a 500 with a stack
// trace instead of a response — the failure this whole path exists to remove.
func isNilPointer(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}

// SendCreatedResponse sends a created response with data
func SendCreatedResponse(c echo.Context, data interface{}, message string) error {
	return SendSuccess(c, http.StatusCreated, data, message)
}

// SendOKResponse sends an OK response with data
func SendOKResponse(c echo.Context, data interface{}, message string) error {
	return SendSuccess(c, http.StatusOK, data, message)
}

// SendNoContentResponse sends a no content response
func SendNoContentResponse(c echo.Context) error {
	return c.NoContent(http.StatusNoContent)
}

// JSON marshals the response to JSON
func (r *APIResponse) JSON() ([]byte, error) {
	return json.Marshal(r)
}
