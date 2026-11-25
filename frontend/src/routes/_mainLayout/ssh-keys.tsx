import { createFileRoute, redirect } from '@tanstack/react-router'

// `/ssh-keys` is superseded by the SSH Keys tab under `/credentials`. Redirect
// to keep old links working.
export const Route = createFileRoute('/_mainLayout/ssh-keys')({
  beforeLoad: () => {
    throw redirect({ to: '/credentials', search: { tab: 'ssh-keys' } as never })
  },
})
