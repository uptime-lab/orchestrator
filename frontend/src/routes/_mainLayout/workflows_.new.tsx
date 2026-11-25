import { createFileRoute } from '@tanstack/react-router'

import { WorkflowEditorPage } from '@/pages/workflowEditor'

import { fetchWorkflowBlocksFx } from '@/entities/workflow'

export const Route = createFileRoute('/_mainLayout/workflows_/new')({
  component: () => <WorkflowEditorPage />,
  loader: async () => {
    await fetchWorkflowBlocksFx()
  },
})
