import { createFileRoute } from '@tanstack/react-router'

import { ForgotPasswordForm } from '@/entities/auth/ui/ForgotPasswordForm'

// Requesting a reset only makes sense while signed out, so this page can live
// under _authLayout and inherit its "bounce signed-in visitors home" rule.
// /reset-password (consuming the link) deliberately does NOT — see that route.
export const Route = createFileRoute('/_authLayout/forgot-password')({
  component: () => <ForgotPasswordForm />,
})
