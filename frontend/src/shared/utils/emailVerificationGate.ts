// The API refuses every request but the confirmation screen's while an
// account created through open registration waits for its owner to confirm
// their address (403, message `email_verification_required`). The session is
// valid, so the answer is a screen, not a refresh.

export const EMAIL_VERIFICATION_REQUIRED = 'email_verification_required'

// The page that hosts the confirmation screen: /verify-email without a token.
export const EMAIL_VERIFICATION_PENDING_PATH = '/verify-email?pending=1'

// emailVerificationRedirect says where to send the browser for a refused
// request, or null: only for this refusal, and never from the page that is
// already showing the screen.
export const emailVerificationRedirect = (
  status: number | undefined,
  message: string | undefined,
  pathname: string,
): string | null =>
  status === 403 &&
  message === EMAIL_VERIFICATION_REQUIRED &&
  !pathname.startsWith('/verify-email')
    ? EMAIL_VERIFICATION_PENDING_PATH
    : null
