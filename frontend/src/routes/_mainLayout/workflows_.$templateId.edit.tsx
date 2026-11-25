import { createFileRoute } from '@tanstack/react-router'

import { WorkflowEditorPage } from '@/pages/workflowEditor'

import {
  fetchWorkflowBlocksFx,
  fetchWorkflowTemplateFx,
} from '@/entities/workflow'

export const Route = createFileRoute(
  '/_mainLayout/workflows_/$templateId/edit',
)({
  component: () => {
    const { templateId } = Route.useParams()
    return <WorkflowEditorPage templateId={templateId} />
  },
  loader: async ({ params }) => {
    await Promise.allSettled([
      fetchWorkflowBlocksFx(),
      fetchWorkflowTemplateFx(params.templateId),
    ])
  },
})
