package results

import (
	"context"
	"errors"
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// clientRefusalSentinels are the plain lookup and state sentinels a handler
// answers with a 4xx of its own choosing (404 for an id outside the account,
// 409 for a state that refuses the request, 400 for a request that must
// change). They predate NotFoundError and carry no status, so they are named
// here; a sentinel that implements OperatorFacingError needs no entry.
var clientRefusalSentinels = []error{
	app_errors.ErrNotFound,
	app_errors.ErrUserNotFound,
	app_errors.ErrAccountNotFound,
	app_errors.ErrMasterTokenNotFound,
	app_errors.ErrAccessEntryNotFound,
	app_errors.ErrAccessEntryUsernameTaken,
	app_errors.ErrImageNotFound,
	app_errors.ErrImageCompressionInProgress,
	app_errors.ErrInvalidImageFile,
	app_errors.ErrNodeCommandNotFound,
	app_errors.ErrNodeAlreadyApproved,
	app_errors.ErrNodeAlreadyRestored,
	app_errors.ErrNodeAlreadyExists,
	app_errors.ErrWorkflowTemplateInvalid,
	app_errors.ErrWorkflowTemplateStale,
	// Profile-update validation, answered 400 by the profile route.
	app_errors.ErrUserNameRequired,
	app_errors.ErrUserNameTooLong,
	app_errors.ErrUserEmailRequired,
	app_errors.ErrUserEmailInvalid,
	// Sign-in and sign-up refusals: a wrong password, a taken address, a
	// wrong invite code, a membership that does not admit the caller.
	app_errors.ErrInvalidCredentials,
	app_errors.ErrUserAlreadyExists,
	app_errors.ErrInvalidInviteCode,
	app_errors.ErrMembershipDisabled,
	app_errors.ErrNotAMember,
	app_errors.ErrTokenInvalid,
	app_errors.ErrTokenExpired,
	app_errors.ErrTwoFactorTooManyAttempts,
}

// IsClientRefusal reports whether err is an expected refusal the API answers
// with a 4xx: a typed OperatorFacingError with a 4xx status, an error marked
// with app_errors.OperatorRefusal, or one of the sentinels above. Such an
// answer is the API working, not the server failing.
func IsClientRefusal(err error) bool {
	if err == nil {
		return false
	}
	var facing OperatorFacingError
	if errors.As(err, &facing) && !isNilPointer(facing) {
		status := facing.HTTPStatus()
		return status >= 400 && status <= 499
	}
	var refusal *app_errors.OperatorRefusalError
	if errors.As(err, &refusal) {
		return true
	}
	if errors.As(err, new(*app_errors.LoginThrottledError)) {
		return true
	}
	for _, sentinel := range clientRefusalSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// LogFailure logs a handler's failed call at the level its answer implies: an
// expected client refusal at WARN, anything else — the server failing — at
// ERROR. ERROR is what an operator is paged or alerted on, so a refusal logged
// there buries the real failures under the ordinary use of the API.
func LogFailure(ctx context.Context, logger *slog.Logger, msg string, err error, args ...any) {
	level := slog.LevelError
	if IsClientRefusal(err) {
		level = slog.LevelWarn
	}
	logger.Log(ctx, level, msg, append([]any{"error", err}, args...)...)
}
