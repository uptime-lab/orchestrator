package account

import (
	"net/http"

	"github.com/google/uuid"
)

// InstanceOperatorAccountError refuses deleting the account that operates
// the install. The operator is not a stored role but the oldest account
// (created_at, then id), so deleting it silently promotes the next-oldest
// account — a tenant — to the one that may read the entitlement's terms and
// apply a file. An operator-side purge must refuse the same account for the
// same reason.
//
// It implements results.OperatorFacingError, so every handler answers it as
// a 409 with its own message rather than a 500.
type InstanceOperatorAccountError struct {
	AccountID uuid.UUID
}

func (e *InstanceOperatorAccountError) Error() string {
	return "this account operates the install (the oldest account, by created_at then id) and cannot be deleted: " +
		"the next-oldest account, a tenant, would become the instance operator"
}

func (e *InstanceOperatorAccountError) HTTPStatus() int   { return http.StatusConflict }
func (e *InstanceOperatorAccountError) ErrorCode() string { return "instance_operator_account" }
