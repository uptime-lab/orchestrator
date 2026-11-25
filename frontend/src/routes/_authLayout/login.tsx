import { createFileRoute, redirect } from '@tanstack/react-router'

import { AuthStatus } from '@/entities/auth/types.ts'
import { AuthBlock } from '@/entities/auth/ui/AuthBlock'

export const Route = createFileRoute('/_authLayout/login')({
  component: () => <AuthBlock type="sign-in" />,
  beforeLoad: async ({ context, search }) => {
    if (context.auth.status === AuthStatus.LoggedIn) {
      throw redirect({
        to: search.redirect ?? '/',
      })
    }
  },
})
