import { t } from 'i18next'

// The error codes the backend's OIDC callback redirects with, worded for the
// person signing in. `provider` is the install's provider name (or the
// generic words). An unknown code reads as the generic failure.
export const oidcCallbackErrorText = (
  code: string,
  provider: string,
): string => {
  switch (code) {
    case 'invite_required':
      return t('entities.auth.oidc.inviteRequired')
    case 'no_account':
      return t('entities.auth.oidc.noAccount')
    case 'account_exists':
      return t('entities.auth.oidc.accountExistsProvider', { provider })
    case 'member_limit':
      return t('entities.auth.oidc.memberLimit')
    case 'identity_conflict':
      return t('entities.auth.oidc.identityConflictProvider', { provider })
    case 'verification_failed':
    case 'invalid_state':
    case 'missing_code':
      return t('entities.auth.oidc.verificationFailed')
    default:
      return t('entities.auth.oidc.failed')
  }
}
