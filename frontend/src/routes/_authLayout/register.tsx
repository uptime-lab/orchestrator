import { createFileRoute, redirect } from '@tanstack/react-router'

import { AuthStatus } from '@/entities/auth/types'
import { AuthBlock } from '@/entities/auth/ui/AuthBlock'
import { forgetSignInOptions } from '@/entities/signInOptions'

export const Route = createFileRoute('/_authLayout/register')({
  component: () => <AuthBlock type="sign-up" />,
  beforeLoad: async ({ context, search }) => {
    if (context.auth.status === AuthStatus.LoggedIn) {
      throw redirect({
        to: search.redirect ?? '/',
      })
    }
    // The registration state moves when the first account is created: a
    // tab that created it and signed out must not still read "first owner".
    forgetSignInOptions()
  },
})
