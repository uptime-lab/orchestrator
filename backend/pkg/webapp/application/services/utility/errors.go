package utility

import (
	"fmt"
	"net/http"
)

// AssignmentCapError is the typed refusal for the per-account assignment cap.
// 409, not 429: the caller's next step is to remove something, not to wait
// (the per-tenant quota doctrine). Implements results.OperatorFacingError so
// every handler — including ones written later — answers it as itself.
type AssignmentCapError struct {
	Max int
}

func (e *AssignmentCapError) Error() string {
	return fmt.Sprintf("this account has reached its limit of %d utility assignments; remove an assignment before adding another", e.Max)
}

// HTTPStatus implements results.OperatorFacingError.
func (e *AssignmentCapError) HTTPStatus() int { return http.StatusConflict }

// ErrorCode implements results.OperatorFacingError.
func (e *AssignmentCapError) ErrorCode() string { return "utility_assignment_cap" }
