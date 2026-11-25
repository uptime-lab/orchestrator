import { combine, createEffect, createStore, sample } from 'effector'
import { useUnit } from 'effector-react'

import { userCommonSocketModel } from '../userCommonSocket'
import { apiWorkflows } from './api'
import type {
  WorkflowBlock,
  WorkflowExecution,
  WorkflowExecutionListParams,
  WorkflowExecutionNode,
  WorkflowStarter,
  WorkflowTemplate,
} from './api/types'
import { scopeOUIDs } from './lib/scope'

// ============= Stores =============

// Block registry: the catalog of step types served by the backend. Static per
// session — fetched once and reused for palette, forms, and display names.
export const $workflowBlocks = createStore<WorkflowBlock[]>([])

// Derived store: map of blocks by node-command slug for step rendering
export const $workflowBlocksBySlug = combine($workflowBlocks, (blocks) => {
  const map: Record<string, WorkflowBlock> = {}
  blocks.forEach((block) => {
    if (block.slug) {
      map[block.slug] = block
    }
  })
  return map
})

export const $workflowTemplates = createStore<WorkflowTemplate[]>([])

// Built-in starter playbook catalog (embedded in the backend)
export const $workflowStarters = createStore<WorkflowStarter[]>([])

// Derived store: map of templates by ID for efficient lookups
export const $workflowTemplatesById = combine(
  $workflowTemplates,
  (templates) => {
    const map: Record<string, WorkflowTemplate> = {}
    templates.forEach((template) => {
      map[template.id] = template
    })
    return map
  },
)

// Current page of the server-paginated executions list (newest first)
export const $workflowExecutions = createStore<WorkflowExecution[]>([])

// Total rows matching the current list filter (for the pager)
export const $workflowExecutionsTotal = createStore<number>(0)

// Derived store: map of executions by ID for efficient lookups
export const $workflowExecutionsById = combine(
  $workflowExecutions,
  (executions) => {
    const map: Record<string, WorkflowExecution> = {}
    executions.forEach((execution) => {
      map[execution.id] = execution
    })
    return map
  },
)

// Individually fetched executions (details sidebar), keyed by id. Separate
// from the paginated $workflowExecutions page so opening a run that is not on
// the current page doesn't distort the page.
export const $workflowExecutionDetails = createStore<
  Record<string, WorkflowExecution>
>({})

// Store for execution nodes by execution ID
export const $workflowExecutionNodes = createStore<
  Record<string, WorkflowExecutionNode[]>
>({})

// ============= Effects =============

export const fetchWorkflowBlocksFx = createEffect(() =>
  apiWorkflows.getWorkflowBlocks(),
)

export const fetchWorkflowTemplatesFx = createEffect(() =>
  apiWorkflows.getWorkflowTemplates(),
)

// Resync the playbook list when the socket reconnects after a drop — a template
// created or changed while the socket was down produced no event.
// The parameterized effects (executions, single items) stay page-driven: a
// blanket refetch without the page's current params would fetch the wrong slice.
sample({
  clock: userCommonSocketModel.events.userSocketReconnected,
  target: fetchWorkflowTemplatesFx,
})

export const fetchWorkflowTemplateFx = createEffect(
  async (templateId: string) => {
    return await apiWorkflows.getWorkflowTemplate(templateId)
  },
)

export const createWorkflowTemplateFx = createEffect(
  async (
    payload: Parameters<typeof apiWorkflows.postCreateWorkflowTemplate>[0],
  ) => {
    return await apiWorkflows.postCreateWorkflowTemplate(payload)
  },
)

export const updateWorkflowTemplateFx = createEffect(
  async ({
    templateId,
    payload,
  }: {
    templateId: string
    payload: Parameters<typeof apiWorkflows.putUpdateWorkflowTemplate>[1]
  }) => {
    return await apiWorkflows.putUpdateWorkflowTemplate(templateId, payload)
  },
)

// Re-targeting is a separate call from a save: see putWorkflowTemplateOU.
// The route takes the whole unit set, so the new set is computed from the
// playbook as the server holds it now — a list row loaded earlier would undo
// a scope change made since. `next` returns null when there is nothing to do.
export const setWorkflowTemplateOUsFx = createEffect(
  async ({
    templateId,
    next,
  }: {
    templateId: string
    next: (current: string[]) => string[] | null
  }) => {
    const current = await apiWorkflows.getWorkflowTemplate(templateId)
    const ouIds = next(scopeOUIDs(current))
    if (ouIds === null) return current
    return await apiWorkflows.putWorkflowTemplateOUs(templateId, ouIds)
  },
)

export const deleteWorkflowTemplateFx = createEffect(
  async (templateId: string) => {
    await apiWorkflows.deleteWorkflowTemplate(templateId)
  },
)

// Built-in starter catalog (static per session, like the block registry)
export const fetchWorkflowStartersFx = createEffect(() =>
  apiWorkflows.getWorkflowTemplateStarters(),
)

// Imports a portable playbook document (an uploaded export or a starter's
// document) as a draft; resolves to the created template plus server warnings
// about references it could not resolve in this account.
export const importWorkflowTemplateFx = createEffect(
  async (
    payload: Parameters<typeof apiWorkflows.postImportWorkflowTemplate>[0],
  ) => {
    return await apiWorkflows.postImportWorkflowTemplate(payload)
  },
)

export const exportWorkflowTemplateFx = createEffect(
  async (templateId: string) => {
    await apiWorkflows.downloadWorkflowTemplateExport(templateId)
  },
)

// Latest-wins guard: page/filter changes can put two list requests in flight
// (no abort in Effector effects); a slow earlier response must not overwrite
// the newer page. Stale results are dropped in the store handlers.
let executionsListSeq = 0

export const fetchWorkflowExecutionsFx = createEffect(
  async (params: WorkflowExecutionListParams = {}) => {
    const seq = ++executionsListSeq
    const result = await apiWorkflows.getWorkflowExecutions(params)
    return { ...result, stale: seq !== executionsListSeq }
  },
)

export const fetchWorkflowExecutionFx = createEffect(
  async (executionId: string) => {
    return await apiWorkflows.getWorkflowExecution(executionId)
  },
)

export const startWorkflowExecutionFx = createEffect(
  async (
    payload: Parameters<typeof apiWorkflows.postStartWorkflowExecution>[0],
  ) => {
    return await apiWorkflows.postStartWorkflowExecution(payload)
  },
)

export const fetchWorkflowExecutionNodesFx = createEffect(
  async (executionId: string) => {
    return await apiWorkflows.getWorkflowExecutionNodes(executionId)
  },
)

// ============= Side effects =============

// Sort function for templates
const sortTemplates = (a: WorkflowTemplate, b: WorkflowTemplate) => {
  return a.name.localeCompare(b.name)
}

// Sort function for executions (newest first)
const sortExecutions = (a: WorkflowExecution, b: WorkflowExecution) => {
  return new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
}

$workflowBlocks.on(fetchWorkflowBlocksFx.doneData, (_, blocks) => blocks)

$workflowTemplates
  .on(fetchWorkflowTemplatesFx.doneData, (_, templates) =>
    [...templates].sort(sortTemplates),
  )
  .on(fetchWorkflowTemplateFx.doneData, (templates, template) => {
    const exists = templates.some((t) => t.id === template.id)
    const updated = exists
      ? templates.map((t) => (t.id === template.id ? template : t))
      : [...templates, template]
    return updated.sort(sortTemplates)
  })
  .on(
    userCommonSocketModel.events.userSocketOnWorkflowTemplateCreated,
    (templates, { payload }) => {
      const exists = templates.some((t) => t.id === payload.id)
      if (exists) {
        return [...templates].sort(sortTemplates)
      }
      return [...templates, payload].sort(sortTemplates)
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnWorkflowTemplateUpdated,
    (templates, { payload }) => {
      const updated = templates.map((t) => (t.id === payload.id ? payload : t))
      return [...updated].sort(sortTemplates)
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnWorkflowTemplateDeleted,
    (templates, { payload }) =>
      templates.filter((t) => t.id !== payload.id).sort(sortTemplates),
  )
  .on(createWorkflowTemplateFx.doneData, (templates, newTemplate) => {
    const exists = templates.some((t) => t.id === newTemplate.id)
    if (exists) {
      return [...templates].sort(sortTemplates)
    }
    return [...templates, newTemplate].sort(sortTemplates)
  })
  .on(setWorkflowTemplateOUsFx.doneData, (templates, updatedTemplate) =>
    templates.map((t) => (t.id === updatedTemplate.id ? updatedTemplate : t)),
  )
  .on(updateWorkflowTemplateFx.doneData, (templates, updatedTemplate) => {
    const updated = templates.map((t) =>
      t.id === updatedTemplate.id ? updatedTemplate : t,
    )
    return [...updated].sort(sortTemplates)
  })
  .on(deleteWorkflowTemplateFx.done, (templates, { params: templateId }) =>
    templates.filter((t) => t.id !== templateId).sort(sortTemplates),
  )
  .on(importWorkflowTemplateFx.doneData, (templates, { template }) => {
    if (templates.some((t) => t.id === template.id)) {
      return [...templates].sort(sortTemplates)
    }
    return [...templates, template].sort(sortTemplates)
  })

$workflowStarters.on(
  fetchWorkflowStartersFx.doneData,
  (_, starters) => starters,
)

$workflowExecutions
  .on(fetchWorkflowExecutionsFx.doneData, (executions, result) =>
    result.stale ? executions : [...result.executions].sort(sortExecutions),
  )
  // Update in place only: the store holds ONE server page; injecting a row
  // fetched for the details sidebar would grow the page past per_page (the
  // sidebar reads $workflowExecutionDetails below instead).
  .on(fetchWorkflowExecutionFx.doneData, (executions, execution) => {
    if (!executions.some((e) => e.id === execution.id)) {
      return executions
    }
    return executions
      .map((e) => (e.id === execution.id ? execution : e))
      .sort(sortExecutions)
  })
  .on(
    userCommonSocketModel.events.userSocketOnWorkflowExecutionUpdated,
    (executions, { payload }) => {
      const updated = executions.map((e) => (e.id === payload.id ? payload : e))
      return [...updated].sort(sortExecutions)
    },
  )
  .on(startWorkflowExecutionFx.doneData, (executions, newExecution) => {
    const exists = executions.some((e) => e.id === newExecution.id)
    if (exists) {
      return [...executions].sort(sortExecutions)
    }
    return [newExecution, ...executions].sort(sortExecutions)
  })

$workflowExecutionDetails
  .on(fetchWorkflowExecutionFx.doneData, (details, execution) => ({
    ...details,
    [execution.id]: execution,
  }))
  .on(
    userCommonSocketModel.events.userSocketOnWorkflowExecutionUpdated,
    (details, { payload }) =>
      details[payload.id] ? { ...details, [payload.id]: payload } : details,
  )

$workflowExecutionsTotal
  .on(fetchWorkflowExecutionsFx.doneData, (total, result) =>
    result.stale ? total : result.total,
  )
  // Keep the pager count roughly honest between refetches when a run is
  // started from this session (WS-updated runs are already counted).
  .on(startWorkflowExecutionFx.doneData, (total) => total + 1)

// Update execution nodes store
$workflowExecutionNodes
  .on(
    userCommonSocketModel.events.userSocketOnWorkflowExecutionNodeUpdated,
    (nodes, { payload }) => {
      const executionId = payload.execution_id
      const existing = nodes[executionId] || []
      // MERGE, never replace. A pushed frame deliberately omits block outputs
      // and other secret-bearing fields (the WS channel has no recipient to
      // check permissions against), so replacing the object wholesale would
      // wipe the richer data a privileged user just fetched over HTTP and
      // leave them watching their own run go blank.
      const updated = existing.map((n) =>
        n.id === payload.id ? { ...n, ...payload } : n,
      )
      // If node doesn't exist, add it
      if (!existing.some((n) => n.id === payload.id)) {
        updated.push(payload)
      }
      return {
        ...nodes,
        [executionId]: updated.sort(
          (a, b) =>
            new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
        ),
      }
    },
  )
  .on(
    fetchWorkflowExecutionNodesFx.done,
    (nodes, { params: executionId, result }) => {
      return {
        ...nodes,
        [executionId]: result.sort(
          (a, b) =>
            new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
        ),
      }
    },
  )

// ============= Helpers =============

// Human-readable name for a step slug: registry name when known, otherwise a
// humanized slug ("status_check" -> "Status Check") so unknown/legacy slugs
// still render sensibly.
export const getWorkflowBlockDisplayName = (
  blocksBySlug: Record<string, WorkflowBlock>,
  slug: string,
): string => {
  const block = blocksBySlug[slug]
  if (block) {
    return block.name
  }
  return slug
    .split('_')
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

// ============= Hooks =============

export const useWorkflowBlocks = () => {
  const blocks = useUnit($workflowBlocks)
  const blocksBySlug = useUnit($workflowBlocksBySlug)
  const fetchBlocks = useUnit(fetchWorkflowBlocksFx)
  return { blocks, blocksBySlug, fetchBlocks }
}

export const useWorkflowTemplates = () => {
  const templates = useUnit($workflowTemplates)
  const fetchTemplates = useUnit(fetchWorkflowTemplatesFx)
  return { templates, fetchTemplates }
}

export const useWorkflowStarters = () => {
  const starters = useUnit($workflowStarters)
  const loading = useUnit(fetchWorkflowStartersFx.pending)
  const fetchStarters = useUnit(fetchWorkflowStartersFx)
  return { starters, loading, fetchStarters }
}

export const useWorkflowTemplate = (templateId: string) => {
  const templatesById = useUnit($workflowTemplatesById)
  const fetchTemplate = useUnit(fetchWorkflowTemplateFx)
  return {
    template: templatesById[templateId],
    fetchTemplate: () => fetchTemplate(templateId),
  }
}

export const useWorkflowExecutions = () => {
  const executions = useUnit($workflowExecutions)
  const total = useUnit($workflowExecutionsTotal)
  const loading = useUnit(fetchWorkflowExecutionsFx.pending)
  const fetchExecutions = useUnit(fetchWorkflowExecutionsFx)
  return { executions, total, loading, fetchExecutions }
}

export const useWorkflowExecution = (executionId: string) => {
  const executionsById = useUnit($workflowExecutionsById)
  const details = useUnit($workflowExecutionDetails)
  const nodes = useUnit($workflowExecutionNodes)
  const fetchExecution = useUnit(fetchWorkflowExecutionFx)
  const fetchNodes = useUnit(fetchWorkflowExecutionNodesFx)
  return {
    // The current page wins (kept fresh by the list fetch + WS); the details
    // store serves runs that are not on the current page.
    execution: executionsById[executionId] ?? details[executionId],
    nodes: nodes[executionId] || [],
    fetchExecution: () => fetchExecution(executionId),
    fetchNodes: () => fetchNodes(executionId),
  }
}
