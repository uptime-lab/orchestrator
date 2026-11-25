import {
  createFileRoute,
  Outlet,
  useNavigate,
  useRouter,
} from '@tanstack/react-router'
import { useEffect } from 'react'

import { authHooks } from '@/entities/auth'
import { AuthStatus } from '@/entities/auth/types'

type AuthLayoutSearch = {
  redirect?: string
}

export const Route = createFileRoute('/_authLayout')({
  component: () => {
    const router = useRouter()
    const navigate = useNavigate()
    const auth = authHooks.useAuthState()
    const search = Route.useSearch()

    useEffect(() => {
      if (auth.status === AuthStatus.LoggedIn) {
        navigate({ to: search.redirect ?? '/' })
      }
    }, [auth.status, search.redirect, navigate])

    useEffect(() => {
      router.invalidate()
    }, [router.invalidate])

    return (
      <div className="flex flex-1">
        <div className="flex flex-[2] flex-col items-center justify-center gap-3 overflow-hidden">
          <Outlet />
        </div>
      </div>
    )
  },
  validateSearch: (search: Record<string, unknown>): AuthLayoutSearch =>
    search.redirect
      ? {
          redirect: String(search.redirect),
        }
      : {},
})
