import { createFileRoute, redirect } from '@tanstack/react-router'

import { SamplePage } from '@/pages/sample'

// The component gallery is a development aid; keep it out of production builds.
export const Route = createFileRoute('/_mainLayout/ui-samples')({
  beforeLoad: () => {
    if (!import.meta.env.DEV) {
      throw redirect({ to: '/', search: {} as never })
    }
  },
  component: () => <SamplePage />,
})
