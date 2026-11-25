import { deleteWorkflowTemplate } from './deleteWorkflowTemplate.ts'
import { getExecutionNodeCommands } from './getExecutionNodeCommands.ts'
import { downloadWorkflowTemplateExport } from './getExportWorkflowTemplate.ts'
import { getWorkflowBlocks } from './getWorkflowBlocks.ts'
import { getWorkflowExecution } from './getWorkflowExecution.ts'
import { getWorkflowExecutionNodes } from './getWorkflowExecutionNodes.ts'
import { getWorkflowExecutions } from './getWorkflowExecutions.ts'
import { getWorkflowTemplate } from './getWorkflowTemplate.ts'
import { getWorkflowTemplates } from './getWorkflowTemplates.ts'
import { getWorkflowTemplateStarters } from './getWorkflowTemplateStarters.ts'
import { postCancelWorkflowExecution } from './postCancelWorkflowExecution.ts'
import { postCreateWorkflowTemplate } from './postCreateWorkflowTemplate.ts'
import { postImportWorkflowTemplate } from './postImportWorkflowTemplate.ts'
import { postSchedulePreview } from './postSchedulePreview.ts'
import { postStartWorkflowExecution } from './postStartWorkflowExecution.ts'
import { putUpdateWorkflowTemplate } from './putUpdateWorkflowTemplate.ts'
import { putWorkflowTemplateOUs } from './putWorkflowTemplateOU.ts'

export const apiWorkflows = {
  getWorkflowBlocks,
  getWorkflowTemplates,
  getWorkflowTemplate,
  postCreateWorkflowTemplate,
  putUpdateWorkflowTemplate,
  putWorkflowTemplateOUs,
  deleteWorkflowTemplate,
  getWorkflowTemplateStarters,
  postImportWorkflowTemplate,
  downloadWorkflowTemplateExport,
  getWorkflowExecutions,
  getWorkflowExecution,
  postStartWorkflowExecution,
  getWorkflowExecutionNodes,
  getExecutionNodeCommands,
  postCancelWorkflowExecution,
  postSchedulePreview,
}

export * from './types.ts'
export type { SchedulePreview } from './postSchedulePreview.ts'
