import { t } from 'i18next'
import { useState, type SyntheticEvent } from 'react'

import { mapApiUserToEntity } from '@/entities/user/api/mapApiUserToEntity'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { authToken } from '@/shared/utils/apiConfig'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { isBlank } from '@/shared/utils/formRules'
import { passwordTooLong } from '@/shared/utils/passwordLimits'

import { apiAuth } from '../api'
import { events } from '../model.ts'
import { AuthBlockContainer } from './AuthBlockContainer.tsx'

type InviteAcceptFormProps = {
  token: string
  preview?: { email: string; account_name: string; role: string }
}

// InviteAcceptForm handles the "new teammate joins via invite link" flow:
// name + password → /auth/invite-signup → tokens stored → logged in. The
// email is fixed to the invited address server-side, so it is not asked for.
export const InviteAcceptForm = ({ token, preview }: InviteAcceptFormProps) => {
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const handleSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setError(null)
    const data = new FormData(event.target as HTMLFormElement)
    const password = (data.get('password') || '') as string
    const name = (data.get('name') || '') as string
    if (isBlank(name)) {
      setError(t('shared.form.nameRequired'))
      return
    }
    if (passwordTooLong(password)) {
      setError(t('shared.password.tooLong'))
      return
    }
    setPending(true)
    try {
      const result = await apiAuth.postInviteSignup({
        token,
        name: name.trim(),
        password,
      })
      authToken.setTokens(result.access_token, result.refresh_token)
      events.signIn({ user: mapApiUserToEntity(result.user) })
    } catch (e: unknown) {
      setPending(false)
      setError(extractApiErrorMessage(e, t('entities.auth.invite.error')))
    }
  }

  return (
    <AuthBlockContainer title={t('entities.auth.invite.title')}>
      <form className="flex flex-col gap-5" onSubmit={handleSubmit}>
        <p className="text-sm text-neutral-7">
          {preview
            ? t('entities.auth.invite.subTitleNamed', {
                email: preview.email,
                account: preview.account_name,
              })
            : t('entities.auth.invite.subTitle')}
        </p>
        <Input
          id="name"
          name="name"
          type="text"
          label={t('entities.auth.invite.name')}
          autoComplete="name"
          required
        />
        <Input
          id="password"
          name="password"
          type="password"
          label={t('entities.auth.invite.password')}
          autoComplete="new-password"
          minLength={8}
          required
          error={!!error}
        />
        <Button
          size="lg"
          type="submit"
          variant={error ? 'destructive' : 'normal'}
          disabled={pending}
        >
          {t('entities.auth.invite.submit')}
        </Button>
        {error && <div className="text-sm text-red-9">{error}</div>}
      </form>
    </AuthBlockContainer>
  )
}
