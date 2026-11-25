import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AuditTrail } from '@/widgets/auditTrail'
import { PasskeysSection } from '@/widgets/passkeysSection'
import { SessionsSection } from '@/widgets/sessionsSection'
import { SidebarEnum } from '@/widgets/sidebars/types.ts'
import { SubscriptionCard } from '@/widgets/subscriptionCard'
import { TwoFactorSection } from '@/widgets/twoFactorSection'

import { apiAccount, type Account } from '@/entities/account/api'
import { currentAccountId } from '@/entities/account/model'
import { authHooks, authModel } from '@/entities/auth'
import { apiAuth } from '@/entities/auth/api'
import {
  completeOidcStepUp,
  getMyIdentities,
  LinkedIdentity,
  startOidcLink,
  unlinkOidcIdentity,
} from '@/entities/auth/api/oidc'
import { storeStepUpTicket } from '@/entities/auth/api/reauth'
import {
  findLinkedIdentity,
  oidcProviderName,
} from '@/entities/auth/installCopy'
import { useOidcConfig } from '@/entities/auth/ui/useOidcConfig'
import { TokenList, useLoadMasterTokens } from '@/entities/masterToken'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { useServerCapabilities } from '@/entities/serverCapabilities'
import { useSignInOptions } from '@/entities/signInOptions'
import { sshKeysModel } from '@/entities/sshKey'
import { getCurrentUser } from '@/entities/user/api/getCurrentUser'
import { putMyPassword } from '@/entities/user/api/putMyPassword'
import { putMyName } from '@/entities/user/api/putMyProfile'
import type { UserEntity } from '@/entities/user/api/types'
import { displayNameProblem } from '@/entities/user/displayName'
import {
  setPreferHostnames,
  usePreferHostnames,
} from '@/entities/user/preferences'
import { getFullName } from '@/entities/user/utils'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { InfoHint } from '@/shared/ui/infoHint'
import { Input } from '@/shared/ui/input'
import { showError, showSuccess } from '@/shared/ui/toast'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { passwordTooLong } from '@/shared/utils/passwordLimits'

type AccountTab = 'account' | 'tokens' | 'audit' | 'subscription' | 'advanced'
type LinkStatus = 'ok' | 'conflict' | 'error'

type AccountSearch = {
  sidebar?: 'masterTokenDetails' | 'createMasterToken'
  tokenId?: string
  tab?: AccountTab
  oidc_link?: LinkStatus
  // oidc_reauth is either a one-time code to exchange for a confirmation
  // ticket, or an error word explaining why the round trip proved nothing.
  oidc_reauth?: string
}

export const Route = createFileRoute('/_mainLayout/account')({
  validateSearch: (search: Record<string, unknown>): AccountSearch => {
    const out: AccountSearch = {}
    if (
      search.sidebar === 'masterTokenDetails' &&
      typeof search.tokenId === 'string'
    ) {
      out.sidebar = 'masterTokenDetails'
      out.tokenId = search.tokenId
    } else if (search.sidebar === 'createMasterToken') {
      out.sidebar = 'createMasterToken'
    }
    if (
      search.tab === 'account' ||
      search.tab === 'tokens' ||
      search.tab === 'audit' ||
      search.tab === 'subscription' ||
      search.tab === 'advanced'
    ) {
      out.tab = search.tab
    }
    if (
      search.oidc_link === 'ok' ||
      search.oidc_link === 'conflict' ||
      search.oidc_link === 'error'
    ) {
      out.oidc_link = search.oidc_link
    }
    if (typeof search.oidc_reauth === 'string' && search.oidc_reauth !== '') {
      out.oidc_reauth = search.oidc_reauth
    }
    return out
  },
  component: () => <AccountPage />,
  // allSettled, and here it is load-bearing rather than tidy: THIS is the page
  // a user is forced to when their account starts mandating a second factor.
  // Neither master tokens nor SSH keys are exempt from that mandate, so under
  // Promise.all both 403s rejected the loader, the route rendered RouteError,
  // and the person could not reach the enrolment screen that would have let
  // them back in. The backend exemption alone does not fix that — the lockout
  // was in this line. Same shape and reasoning as credentials.tsx.
  loader: async () => {
    // The token list is fetched by the page once the permission map says the
    // viewer may read it (useLoadMasterTokens).
    await Promise.allSettled([sshKeysModel.fetchSSHKeysFx()])
  },
})

const AccountPage = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = Route.useSearch()
  const canReadAudit = useCan()(Actions.AuditRead)
  // Same posture as the audit tab: the card names the install's customer
  // and cap, which is not a per-account read.
  const canManageEntitlement = useCan()(Actions.EntitlementManage)
  // Master tokens, list included, need the right to manage them.
  const canManageTokens = useCan()(Actions.MasterTokenManage)
  useLoadMasterTokens(canManageTokens)
  const tab: AccountTab = search.tab ?? 'account'

  const setTab = useCallback(
    (next: AccountTab) => {
      navigate({ to: '.', search: (prev) => ({ ...prev, tab: next }) })
    },
    [navigate],
  )

  return (
    <div className="h-full w-full overflow-y-auto p-4 pt-8 md:p-8">
      <h1 className="text-xl uppercase font-bold mb-4">
        {t('routes.account.title')}
      </h1>

      <div className="flex gap-1 border-b border-neutral-11 mb-8">
        <TabButton
          active={tab === 'account'}
          onClick={() => setTab('account')}
          label={t('routes.account.tabAccount')}
        />
        {canManageTokens && (
          <TabButton
            active={tab === 'tokens'}
            onClick={() => setTab('tokens')}
            label={t('routes.account.tabTokens')}
          />
        )}
        {/* Gated on the same right the route needs. audit.read is held by no
            role below super_admin, so offering the tab to anyone else is an
            invitation to a 403 — the shape the playbook action menu was fixed
            for. */}
        {canReadAudit && (
          <TabButton
            active={tab === 'audit'}
            onClick={() => setTab('audit')}
            label={t('routes.account.tabAudit')}
          />
        )}
        {canManageEntitlement && (
          <TabButton
            active={tab === 'subscription'}
            onClick={() => setTab('subscription')}
            label={t('routes.account.tabSubscription')}
          />
        )}
        <TabButton
          active={tab === 'advanced'}
          onClick={() => setTab('advanced')}
          label={t('routes.account.tabAdvanced')}
        />
      </div>

      {tab === 'audit' && canReadAudit && <AuditTrail />}

      {tab === 'account' && (
        <AccountTab linkStatus={search.oidc_link} stepUp={search.oidc_reauth} />
      )}
      {tab === 'tokens' && canManageTokens && <TokensTab />}
      {tab === 'subscription' && canManageEntitlement && <SubscriptionCard />}
      {tab === 'advanced' && <AdvancedTab />}
    </div>
  )
}

const TabButton = ({
  active,
  onClick,
  label,
}: {
  active: boolean
  onClick: () => void
  label: string
}) => (
  <button
    type="button"
    onClick={onClick}
    className={
      'px-4 py-2 text-sm uppercase tracking-wide border-b-2 -mb-px transition-colors ' +
      (active
        ? 'border-lime-6 text-lime-6'
        : 'border-transparent text-neutral-7 hover:text-neutral-5')
    }
  >
    {label}
  </button>
)

// forcedTwoFactorEnrollment reports whether the user landed here because the
// API refused them until they enroll (see the 403 handler in apiConfig).
const forcedTwoFactorEnrollment = () =>
  new URLSearchParams(window.location.search).get('enroll_2fa') === '1'

// ============= Email address + verification =============

// EmailVerificationSection shows whether the address is confirmed and offers a
// resend. Verifying matters beyond hygiene: it is what lets a later provider
// sign-in attach to this account silently instead of asking for the password.
//
// On an install that cannot send mail there is no confirmation to send, so
// the section is not shown at all rather than offering a button that fails.
const EmailVerificationSection = () => {
  const { t } = useTranslation()
  const { config: oidc } = useOidcConfig()
  const { options } = useSignInOptions()
  const [user, setUser] = useState<UserEntity | null>(null)
  const [pending, setPending] = useState(false)
  const [status, setStatus] = useState<{
    kind: 'ok' | 'error'
    text: string
  } | null>(null)

  useEffect(() => {
    getCurrentUser()
      .then(setUser)
      .catch(() => setUser(null))
  }, [])

  if (!user || user.email_verified) return null
  if (options?.email_delivery === false) return null

  const handleResend = async () => {
    setPending(true)
    setStatus(null)
    try {
      await apiAuth.postResendVerification()
      setStatus({
        kind: 'ok',
        text: t('routes.account.emailSent'),
      })
    } catch (err: unknown) {
      setStatus({
        kind: 'error',
        text: extractApiErrorMessage(err, t('routes.account.emailSendError')),
      })
    } finally {
      setPending(false)
    }
  }

  return (
    <section className="border border-neutral-10 rounded p-4">
      <h2 className="text-sm uppercase font-bold">
        {t('routes.account.emailTitle')}
      </h2>
      <p className="text-sm text-neutral-4 mt-1">
        {t('routes.account.emailNotConfirmed', { email: user.email })}
      </p>
      {oidc && (
        <p className="text-xs text-neutral-7 mt-1">
          {t('routes.account.emailHintProvider', {
            provider: oidcProviderName(oidc),
          })}
        </p>
      )}
      <div className="mb-3" />
      <Button
        size="md"
        kind="secondary"
        onClick={handleResend}
        disabled={pending}
      >
        {t('routes.account.emailSendButton')}
      </Button>
      {status && (
        <div
          className={
            'mt-3 text-xs ' +
            (status.kind === 'ok' ? 'text-lime-6' : 'text-red-9')
          }
        >
          {status.text}
        </div>
      )}
    </section>
  )
}

// The signed-in user's display name: the name members and the audit trail
// show. Only the name is sent; the email stays as it is.
const DisplayNameSection = () => {
  const { t } = useTranslation()
  const user = authHooks.useCurrentUser()
  const current = user
    ? [user.name, user.surname].filter(Boolean).join(' ')
    : ''
  const [name, setName] = useState(current)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Follow the stored name until the user starts editing.
  useEffect(() => {
    setName(current)
  }, [current])

  if (!user) return null

  const problem = displayNameProblem(name)
  const unchanged = name.trim() === current

  const handleSave = async (e: React.SyntheticEvent) => {
    e.preventDefault()
    if (problem || unchanged) return
    setPending(true)
    setError(null)
    try {
      const updated = await putMyName(name.trim())
      authModel.events.currentUserUpdated({ user: { ...user, ...updated } })
      showSuccess(t('routes.account.nameSaved', { name: getFullName(updated) }))
    } catch (err: unknown) {
      setError(extractApiErrorMessage(err, t('routes.account.nameError')))
    } finally {
      setPending(false)
    }
  }

  return (
    <section>
      <h2 className="text-lg uppercase font-bold">
        {t('routes.account.nameTitle')}
      </h2>
      <form onSubmit={handleSave} className="mt-3 flex items-end gap-3">
        <div className="flex-1 min-w-0">
          <Input
            id="display-name"
            name="name"
            label={t('routes.account.nameLabel')}
            value={name}
            onChange={(e) => {
              setName(e.target.value)
              setError(null)
            }}
            error={problem !== null}
            maxLength={256}
            autoComplete="name"
          />
        </div>
        <Button
          size="lg"
          type="submit"
          disabled={pending || problem !== null || unchanged}
        >
          {t('routes.account.nameSave')}
        </Button>
      </form>
      {problem && (
        <div className="text-xs text-red-9 mt-1">
          {problem === 'required'
            ? t('routes.account.nameRequired')
            : t('routes.account.nameTooLong')}
        </div>
      )}
      {error && <div className="text-xs text-red-9 mt-1">{error}</div>}
    </section>
  )
}

// ============= Account tab: sign-in methods + password =============

const AccountTab = ({
  linkStatus,
  stepUp,
}: {
  linkStatus?: LinkStatus
  stepUp?: string
}) => {
  const { t } = useTranslation()
  const { config: oidc } = useOidcConfig()
  const navigate = useNavigate()
  const [identities, setIdentities] = useState<LinkedIdentity[]>([])
  const [showPassword, setShowPassword] = useState(false)
  const [stepUpState, setStepUpState] = useState<
    'idle' | 'ok' | 'mismatch' | 'error'
  >('idle')
  const [unlinking, setUnlinking] = useState(false)
  // Unlinking asks first: signing in with that provider stops working.
  const [confirmUnlink, setConfirmUnlink] = useState<string | null>(null)
  const [unlinkError, setUnlinkError] = useState<string | null>(null)

  useEffect(() => {
    getMyIdentities()
      .then(setIdentities)
      .catch(() => setIdentities([]))
  }, [])

  // Unlink is the remedy half of "a linked identity is a permanent credential".
  // The refusal that matters is the 409: removing the last way in. Its message
  // is written for the user by the backend, so it is shown as sent rather than
  // reworded — the wrong local wording here would tell someone to add a factor
  // that does not actually let them sign in.
  const unlinkIdentity = async (id: string) => {
    setUnlinking(true)
    setUnlinkError(null)
    try {
      await unlinkOidcIdentity(id)
      setIdentities(await getMyIdentities())
    } catch (e) {
      const msg = (
        e as { response?: { data?: { message?: string; error?: string } } }
      ).response?.data
      setUnlinkError(
        msg?.message || msg?.error || t('routes.account.unlinkError'),
      )
    } finally {
      setUnlinking(false)
    }
  }

  // Returning from the identity provider. The backend put the confirmation
  // behind a one-time code rather than in the URL, so the ticket itself never
  // lands in browser history; exchange it, then strip the param so a reload
  // does not look like a second confirmation.
  useEffect(() => {
    if (!stepUp) return
    const clear = () =>
      navigate({
        to: '.',
        search: (prev) => ({ ...prev, oidc_reauth: undefined }),
        replace: true,
      })
    if (stepUp === 'mismatch' || stepUp === 'error') {
      setStepUpState(stepUp)
      clear()
      return
    }
    completeOidcStepUp(stepUp)
      .then((res) => {
        storeStepUpTicket(res.ticket, res.expires_in)
        setStepUpState('ok')
      })
      .catch(() => setStepUpState('error'))
      .finally(clear)
  }, [stepUp, navigate])

  // The identity linked through THIS install's provider, matched by issuer.
  // Matching on Google's issuer here once hid every link made through any
  // other provider.
  const linked = findLinkedIdentity(identities, oidc)
  const provider = oidcProviderName(oidc)

  return (
    <div className="flex flex-col gap-8 max-w-xl">
      {stepUpState === 'ok' && (
        <div className="text-xs text-lime-6">
          {t('routes.account.stepUpOk')}
        </div>
      )}
      {stepUpState === 'mismatch' && (
        <div className="text-xs text-red-9">
          {t('routes.account.stepUpMismatch')}
        </div>
      )}
      {stepUpState === 'error' && (
        <div className="text-xs text-red-9">
          {t('routes.account.stepUpError')}
        </div>
      )}

      <DisplayNameSection />
      <EmailVerificationSection />
      <TwoFactorSection required={forcedTwoFactorEnrollment()} />
      <PasskeysSection />
      <SessionsSection />

      {/* Sign-in methods */}
      {oidc && (
        <section>
          <h2 className="text-lg uppercase font-bold">
            {t('routes.account.signInTitle')}
          </h2>
          <p className="text-sm text-neutral-4 mt-1 mb-4">
            {t('routes.account.signInHintProvider', { provider })}
          </p>

          {linkStatus === 'ok' && (
            <div className="mb-3 text-xs text-lime-6">
              {t('routes.account.linkOkProvider', { provider })}
            </div>
          )}
          {linkStatus === 'conflict' && (
            <div className="mb-3 text-xs text-red-9">
              {t('routes.account.linkConflictProvider', { provider })}
            </div>
          )}
          {linkStatus === 'error' && (
            <div className="mb-3 text-xs text-red-9">
              {t('routes.account.linkError')}
            </div>
          )}
          {unlinkError && (
            <div className="mb-3 text-xs text-red-9">{unlinkError}</div>
          )}

          <div className="flex items-center justify-between border border-neutral-10 rounded px-3 py-3">
            <div className="min-w-0">
              <div className="text-sm text-neutral-4">
                {oidcProviderName(oidc, true)}
              </div>
              {linked ? (
                <div className="text-xs text-neutral-7 truncate">
                  {t('routes.account.linkedWithEmail', {
                    emailSuffix: linked.email ? ` — ${linked.email}` : '',
                  })}
                </div>
              ) : (
                <div className="text-xs text-neutral-7">
                  {t('routes.account.notLinked')}
                </div>
              )}
            </div>
            {linked ? (
              <div className="flex items-center gap-3">
                <span className="text-xxs uppercase tracking-widest text-lime-6">
                  {t('routes.account.linkedBadge')}
                </span>
                <Button
                  size="md"
                  kind="secondary"
                  disabled={unlinking}
                  onClick={() => setConfirmUnlink(linked.id)}
                >
                  {unlinking
                    ? t('routes.account.unlinking')
                    : t('routes.account.unlink')}
                </Button>
              </div>
            ) : (
              <Button
                size="md"
                kind="secondary"
                onClick={() => startOidcLink()}
              >
                {t('routes.account.linkProvider', { provider })}
              </Button>
            )}
          </div>
        </section>
      )}

      {/* Password (collapsible) */}
      <section>
        <button
          type="button"
          onClick={() => setShowPassword((v) => !v)}
          className="flex items-center gap-2 text-lg uppercase font-bold"
        >
          {t('routes.account.passwordTitle')}
          <span className="text-neutral-7 text-xs">
            {showPassword ? '▲' : '▼'}
          </span>
        </button>
        <p className="text-sm text-neutral-4 mt-1">
          {oidc
            ? t('routes.account.passwordHintProvider', { provider })
            : t('routes.account.passwordHintPlain')}
        </p>
        {showPassword && <PasswordForm />}
      </section>
      <ConfirmDialog
        open={confirmUnlink !== null}
        title={t('routes.account.unlinkTitle')}
        message={t('routes.account.unlinkMessage', { provider })}
        confirmText={t('routes.account.unlink')}
        destructive
        busy={unlinking}
        onConfirm={() => {
          const id = confirmUnlink
          setConfirmUnlink(null)
          if (id) void unlinkIdentity(id)
        }}
        onCancel={() => setConfirmUnlink(null)}
      />
    </div>
  )
}

const PasswordForm = () => {
  const { t } = useTranslation()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [status, setStatus] = useState<{
    kind: 'error' | 'ok'
    text: string
  } | null>(null)
  const [pending, setPending] = useState(false)

  const handleSubmit = async (e: React.SyntheticEvent) => {
    e.preventDefault()
    setStatus(null)
    if (password.length < 8) {
      setStatus({
        kind: 'error',
        text: t('routes.account.passwordTooShort'),
      })
      return
    }
    if (passwordTooLong(password)) {
      setStatus({ kind: 'error', text: t('shared.password.tooLong') })
      return
    }
    if (password !== confirm) {
      setStatus({ kind: 'error', text: t('routes.account.passwordMismatch') })
      return
    }
    setPending(true)
    try {
      await putMyPassword(password)
      setPassword('')
      setConfirm('')
      setStatus({ kind: 'ok', text: t('routes.account.passwordSaved') })
      // A password change ends every session, this one included; sign out
      // here, after the notice has been read, instead of at the next renewal.
      window.setTimeout(() => authModel.events.signOut(), 4000)
    } catch (err: unknown) {
      setStatus({
        kind: 'error',
        text: extractApiErrorMessage(
          err,
          t('routes.account.passwordSaveError'),
        ),
      })
    } finally {
      setPending(false)
    }
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-3 mt-4 max-w-sm">
      <Input
        id="new-password"
        name="new-password"
        type="password"
        label={t('routes.account.newPasswordLabel')}
        autoComplete="new-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      <Input
        id="confirm-password"
        name="confirm-password"
        type="password"
        label={t('routes.account.confirmPasswordLabel')}
        autoComplete="new-password"
        value={confirm}
        onChange={(e) => setConfirm(e.target.value)}
      />
      <div className="flex items-center gap-3">
        <Button size="md" type="submit" disabled={pending}>
          {t('routes.account.savePassword')}
        </Button>
        {status && (
          <span
            className={
              status.kind === 'ok'
                ? 'text-xs text-lime-6'
                : 'text-xs text-red-9'
            }
          >
            {status.text}
          </span>
        )}
      </div>
    </form>
  )
}

// ============= Master Tokens tab =============

const TokensTab = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = Route.useSearch()

  const handleTokenClick = useCallback(
    (tokenId: string) => {
      navigate({
        to: '.',
        search: (prev) => ({
          ...prev,
          sidebar: SidebarEnum.MasterTokenDetails,
          tokenId,
        }),
      })
    },
    [navigate],
  )
  const handleCreateClick = useCallback(() => {
    navigate({
      to: '.',
      search: (prev) => ({ ...prev, sidebar: SidebarEnum.CreateMasterToken }),
    })
  }, [navigate])

  const selectedTokenId =
    search.sidebar === SidebarEnum.MasterTokenDetails && search.tokenId
      ? search.tokenId
      : undefined

  return (
    <section>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-lg uppercase font-bold">
            {t('routes.account.tokensTitle')}
          </h2>
          <p className="text-sm text-neutral-4 mt-1">
            {t('routes.account.tokensHint')}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="md" onClick={handleCreateClick}>
            {t('routes.account.createToken')}
          </Button>
        </div>
      </div>
      <TokenList
        selectedTokenId={selectedTokenId}
        onTokenClick={handleTokenClick}
      />
    </section>
  )
}

// ============= Advanced tab: personal preferences =============

const IdentifyDurationSection = () => {
  const { t } = useTranslation()
  // The /auth/accounts listing carries no settings, so the value comes from
  // GET /accounts/:id (readable by any member; the PUT is super_admin-only).
  const [account, setAccount] = useState<Account | null>(null)
  const canUpdate = useCan()(Actions.AccountUpdate)
  const [seconds, setSeconds] = useState(300)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    const accountId = currentAccountId()
    if (!accountId) return
    apiAccount
      .getAccount(accountId)
      .then((acc) => {
        setAccount(acc)
        if (acc.identify_duration_sec) setSeconds(acc.identify_duration_sec)
      })
      .catch(() => {})
  }, [])
  if (!account) return null
  const save = async () => {
    if (pending) return
    setPending(true)
    try {
      const updated = await apiAccount.putAccountIdentifyDuration(
        account.id,
        seconds,
      )
      setAccount(updated)
      showSuccess(t('routes.account.identifySaved'))
    } catch {
      showError(t('routes.account.identifySaveError'))
    } finally {
      setPending(false)
    }
  }
  return (
    <section className="mb-8">
      <h2 className="text-lg uppercase font-bold">
        {t('routes.account.identifyTitle')}
      </h2>
      <p className="text-sm text-neutral-4 mt-1 mb-4">
        {t('routes.account.identifyHint')}
        <span className="whitespace-nowrap">
          &nbsp;
          <InfoHint text={t('routes.account.identifyInfoHint')} />
        </span>
      </p>
      <div className="flex items-center gap-3">
        <input
          type="number"
          min={10}
          max={3600}
          value={seconds}
          disabled={!canUpdate}
          onChange={(e) => setSeconds(Number(e.target.value))}
          className="w-28 bg-neutral-11 border border-neutral-8 rounded px-2 py-1.5 text-sm text-neutral-2"
        />
        <span className="text-sm text-neutral-4">
          {t('routes.account.identifySecondsLabel')}
        </span>
        <button
          type="button"
          disabled={!canUpdate || pending || seconds < 10 || seconds > 3600}
          onClick={save}
          className="px-3 py-1.5 text-sm rounded border border-lime-8 bg-lime-9/30 text-lime-4 hover:bg-lime-8/40 disabled:opacity-50"
        >
          {t('routes.account.save')}
        </button>
      </div>
      {!canUpdate && (
        <p className="text-xs text-neutral-5 mt-2">
          {t('routes.account.identifyOwnerOnly')}
        </p>
      )}
    </section>
  )
}

const AdvancedTab = () => {
  const { t } = useTranslation()
  const preferHostnames = usePreferHostnames()
  // The platform release the server was built for and the commit it was
  // built from, each when its build recorded one. A build from source has a
  // revision and no version, and shows the build only.
  const { revision, version } = useServerCapabilities()
  return (
    <section>
      <IdentifyDurationSection />
      <h2 className="text-lg uppercase font-bold">
        {t('routes.account.preferencesTitle')}
      </h2>
      <p className="text-sm text-neutral-4 mt-1 mb-4">
        {t('routes.account.preferencesHint')}
      </p>
      <label className="flex items-center gap-2.5 text-sm text-neutral-2 cursor-pointer select-none">
        <input
          type="checkbox"
          checked={preferHostnames}
          onChange={(e) => setPreferHostnames(e.target.checked)}
          className="w-4 h-4 accent-lime-6 cursor-pointer"
        />
        {t('routes.account.preferHostnames')}
      </label>
      {(version || revision) && (
        <p className="text-xs text-neutral-6 mt-8">
          {version && (
            <>
              {t('routes.account.serverVersion')}{' '}
              <span className="font-mono text-neutral-4">{version}</span>
            </>
          )}
          {version && revision && ' · '}
          {revision && (
            <>
              {version
                ? t('routes.account.serverBuildAfterVersion')
                : t('routes.account.serverBuild')}{' '}
              <span className="font-mono text-neutral-4" title={revision}>
                {revision.slice(0, 12)}
              </span>
            </>
          )}
        </p>
      )}
    </section>
  )
}
