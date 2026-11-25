import { createFileRoute, redirect } from '@tanstack/react-router'

// `/settings` duplicated `/account` (master tokens). Consolidated into
// `/account`; keep the path as a redirect for any existing bookmarks.
export const Route = createFileRoute('/_mainLayout/settings')({
  beforeLoad: () => {
    throw redirect({ to: '/account', search: {} as never })
  },
})
