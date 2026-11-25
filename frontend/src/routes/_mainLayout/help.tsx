import { createFileRoute, redirect } from '@tanstack/react-router'

// `/help` has no page of its own. Kept as a redirect rather than deleted: the
// path may be linked from outside the app, and landing on the home page beats a
// not-found screen.
export const Route = createFileRoute('/_mainLayout/help')({
  beforeLoad: () => {
    throw redirect({ to: '/', search: {} as never })
  },
})
