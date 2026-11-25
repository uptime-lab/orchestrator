import { createFileRoute, redirect } from '@tanstack/react-router'

// `/events` has no real feed behind it and is not linked from the nav.
// Redirect to the nodes list until a real events feed exists.
export const Route = createFileRoute('/_mainLayout/events')({
  beforeLoad: () => {
    throw redirect({ to: '/', search: {} as never })
  },
})
