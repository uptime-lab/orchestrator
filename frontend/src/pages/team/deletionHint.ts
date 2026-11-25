import { t } from 'i18next'

// deletionRequestHint words the account-deletion section. `accountErasure`
// is the server capability: true where the operator console performs the
// erasure (the hosted install), false on a self-hosted install, which only
// records the request for its operator. Unknown (capabilities not loaded, or
// an older backend) keeps the original wording.
export const deletionRequestHint = (accountErasure: boolean | undefined) =>
  accountErasure === false
    ? t('pages.team.deleteAccount.hintSelfHosted')
    : t('pages.team.deleteAccount.hint')

// deletionRequestTooltip is the background behind the hint's info mark, on
// the same capability: only the hosted install has an operator console that
// performs an erasure, so a self-hosted install must not describe one.
export const deletionRequestTooltip = (accountErasure: boolean | undefined) =>
  accountErasure === false
    ? t('pages.team.deleteAccount.hintTooltipSelfHosted')
    : t('pages.team.deleteAccount.hintTooltip')

// resentInviteHint: a resend mints a new link and mails it only where the
// install can send mail. Without mail, saying it "was sent" leaves the
// invitee waiting for nothing.
export const resentInviteHint = (delivered: boolean) =>
  delivered
    ? t('pages.team.invite.resentHint')
    : t('pages.team.invite.resentHintNotDelivered')

// createdInviteHint: the line above a freshly created invite link. Without
// mail it must say the link was not emailed, as the resend line does, or the
// inviter waits for a mail that never left.
export const createdInviteHint = (delivered: boolean, email: string | null) =>
  delivered
    ? t('pages.team.invite.sentHint', { email })
    : t('pages.team.invite.linkHint')

// pendingInviteDelivery: what a pending row can say about delivery. The list
// does not record whether each invite was mailed; an install that cannot send
// mail at all mailed none of them. Unknown (options not loaded) says nothing.
export const pendingInviteNotEmailed = (
  emailDelivery: boolean | undefined,
): boolean => emailDelivery === false
