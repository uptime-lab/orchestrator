import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { t } from 'i18next'
import { useEffect, useState } from 'react'

import { useLoginChallenge } from '@/entities/auth/hooks.ts'
import { oidcProviderName } from '@/entities/auth/installCopy.ts'
import { completeOidcFx, linkExistingAccountFx } from '@/entities/auth/model.ts'
import { oidcCallbackErrorText } from '@/entities/auth/oidcCallbackError.ts'
import { oidcCodeStartedHere } from '@/entities/auth/oidcFlowMarker.ts'
import { signInErrorMessage } from '@/entities/auth/signInError.ts'
import { AuthBlockContainer } from '@/entities/auth/ui/AuthBlockContainer.tsx'
import { useOidcConfig } from '@/entities/auth/ui/useOidcConfig.ts'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

type CallbackSearch = {
  code?: string
  error?: string
  link_code?: string
}

// OidcCallback consumes the one-time handoff code from the IdP round-trip:
// success (single account) logs the user in (the layout then routes home),
// a multi-account result shows the account picker on /login, and any error is
// shown with a link back to login.
//
// A `link_code` instead means the provider account's email matches an existing
// local account whose mailbox ownership was never proven. Rather than bouncing
// the user, we ask for that account's password: the IdP has proven the mailbox
// and the password proves the account, so the two link in one step.
const OidcCallback = () => {
  const navigate = useNavigate()
  const challenge = useLoginChallenge()
  const { code, error, link_code: linkCode } = Route.useSearch()
  const { config } = useOidcConfig()
  const provider = oidcProviderName(config)
  // A failure reported while completing; the redirect's own error code is
  // worded at render, so the provider's name can arrive after the page.
  const [failure, setFailure] = useState<string | null>(null)
  // A code is only spent by the tab that went to the provider for it; one
  // that arrives in any other page load is someone else's sign-in.
  const handoff = linkCode ?? code
  const notStartedHere = !error && !!handoff && !oidcCodeStartedHere(handoff)
  const message =
    failure ??
    (error
      ? oidcCallbackErrorText(error, provider)
      : notStartedHere
        ? t('entities.auth.oidc.notStartedHere')
        : null)

  useEffect(() => {
    if (error || linkCode || !code || notStartedHere) return
    completeOidcFx(code).catch((e: unknown) => {
      setFailure(extractApiErrorMessage(e, t('entities.auth.oidc.failed')))
    })
  }, [code, error, linkCode, notStartedHere])

  // Multi-account: the picker lives on /login (reads the same challenge store).
  useEffect(() => {
    if (challenge) navigate({ to: '/login' })
  }, [challenge, navigate])

  if (linkCode && !message) {
    return (
      <AuthBlockContainer title={t('entities.auth.oidc.linkExisting.title')}>
        <LinkExistingForm
          linkCode={linkCode}
          provider={provider}
          onFailed={setFailure}
        />
      </AuthBlockContainer>
    )
  }

  return (
    <AuthBlockContainer title={t('entities.auth.oidc.title')}>
      {message ? (
        <div className="flex flex-col gap-4">
          <div className="text-sm text-red-9">{message}</div>
          <a href="/login" className="text-sm text-lime-6">
            {t('entities.auth.oidc.backToLogin')}
          </a>
        </div>
      ) : (
        <div className="text-sm text-neutral-7">
          {t('entities.auth.oidc.completing')}
        </div>
      )}
    </AuthBlockContainer>
  )
}

type LinkExistingFormProps = {
  linkCode: string
  provider: string
  // onFailed reports a terminal failure: the one-time code is single-use, so a
  // rejected password cannot be retried here — the user restarts the flow.
  onFailed: (message: string) => void
}

const LinkExistingForm = ({
  linkCode,
  provider,
  onFailed,
}: LinkExistingFormProps) => {
  const [password, setPassword] = useState('')
  const [pending, setPending] = useState(false)

  const handleSubmit = async (e: React.SyntheticEvent) => {
    e.preventDefault()
    if (pending || !password) return
    setPending(true)
    try {
      await linkExistingAccountFx({ linkCode, password })
    } catch (err: unknown) {
      onFailed(
        signInErrorMessage(err, t('entities.auth.oidc.linkExisting.failed')),
      )
    } finally {
      setPending(false)
    }
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-4">
      <div className="text-sm text-neutral-6">
        {t('entities.auth.oidc.linkExisting.hintProvider', { provider })}
      </div>
      <Input
        id="link-password"
        name="password"
        type="password"
        label={t('entities.auth.oidc.linkExisting.password')}
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        autoFocus
        required
      />
      <Button size="lg" type="submit" disabled={pending || !password}>
        {t('entities.auth.oidc.linkExisting.submit')}
      </Button>
      <a href="/login" className="text-sm text-lime-6">
        {t('entities.auth.oidc.backToLogin')}
      </a>
    </form>
  )
}

export const Route = createFileRoute('/_authLayout/oidc-callback')({
  component: OidcCallback,
  validateSearch: (search: Record<string, unknown>): CallbackSearch => ({
    code: search.code ? String(search.code) : undefined,
    error: search.error ? String(search.error) : undefined,
    link_code: search.link_code ? String(search.link_code) : undefined,
  }),
})
