// Workflow block registry types (served by GET /api/v1/workflow-blocks).
// The registry is the single source of truth for available step types,
// display names, and parameter forms.
export type WorkflowBlockParamType =
  | 'string'
  | 'text'
  | 'int'
  | 'bool'
  | 'enum'
  | 'os_image'
  | 'ssh_key'
  | 'boot_order'
  | 'expr'
  | 'cron'

export type WorkflowBlockParam = {
  name: string
  label: string
  description?: string
  // Longer, read-once background shown behind an info hint, never inline.
  details?: string
  type: WorkflowBlockParamType
  required: boolean
  default?: unknown
  enum_values?: string[]
  placeholder?: string
  // A numeric parameter's bounds, when the registry declares them.
  min?: number
  max?: number
}

export type WorkflowBlockPort = {
  name: string
  label?: string
}

export type WorkflowBlockExecutor = 'agent' | 'server' | 'plugin'

export type WorkflowBlock = {
  type: string
  slug?: string
  executor: WorkflowBlockExecutor
  category: string
  name: string
  description?: string
  // Longer, read-once background shown behind an info hint, never inline.
  details?: string
  params: WorkflowBlockParam[]
  inputs: WorkflowBlockPort[]
  outputs: WorkflowBlockPort[]
  dangerous: boolean
  reboots_node: boolean
  // The device boot state this block needs. '' or absent = either.
  // 'managed_os' is a CLAIM nothing enforces; 'deferred_to_managed_os' is the
  // platform holding the command until the OS boots (information, not a
  // problem); 'recovery' is refused by the agent on a live OS.
  boot_requirement?: '' | 'managed_os' | 'deferred_to_managed_os' | 'recovery'
  // The case the requirement cannot express — a precondition that is really
  // about a parameter (which disk) rather than the boot mode.
  boot_note?: string
}

// Workflow graph types (node-graph definition of a template)
export type WorkflowGraphPosition = {
  x: number
  y: number
}

// Per-block execution policy (retries / timeout / on-error behavior).
export type WorkflowBlockPolicy = {
  retries?: number
  timeout_seconds?: number
  on_error?: 'fail' | 'continue'
}

export type WorkflowGraphNode = {
  id: string
  type: string // block type from the registry, e.g. "trigger.start", "command.reboot"
  name?: string
  description?: string
  params?: Record<string, unknown>
  policy?: WorkflowBlockPolicy
  position: WorkflowGraphPosition
}

export type WorkflowGraphEdge = {
  id?: string
  from: string
  to: string
  from_port?: string
}

export type WorkflowGraph = {
  schema_version: number
  nodes: WorkflowGraphNode[]
  edges: WorkflowGraphEdge[]
}

// Portable playbook document: the account-independent JSON produced by the
// export endpoint, consumed by the import endpoint, and carried by built-in
// starters. Account-specific ids (images, keys, OUs) are never part of it.
export type PortablePlaybookDocument = {
  kind: string // "orchestrator.playbook"
  schema_version: number
  name: string
  description?: string
  graph: WorkflowGraph
  exported_at?: string
}

// One entry of the built-in starter catalog; instantiate by posting the
// document to the import endpoint.
export type WorkflowStarter = {
  id: string
  category: string
  name: string
  description: string
  block_types: string[]
  document: PortablePlaybookDocument
}

// Workflow template types
export type CommandSpec = {
  slug: string
  payload: Record<string, unknown>
}

export type WorkflowStep = {
  order: number
  command: CommandSpec
  name?: string
  description?: string
}

export type WorkflowTemplate = {
  id: string
  account_id: string
  name: string
  description: string
  is_shortcut?: boolean
  steps: WorkflowStep[]
  // Always present in API responses: synthesized from steps for legacy templates
  graph?: WorkflowGraph
  // Scopes the template to an organizational unit; empty = all nodes
  // Legacy single-unit field: present only when the playbook is scoped to
  // exactly ONE unit. Read ou_ids instead — a playbook spanning several
  // reports this empty, deliberately.
  ou_id?: string
  // The units this playbook is available to, each with its child units.
  // Empty = every node in the account.
  ou_ids?: string[]
  // Drafts are hidden from run pickers; legacy rows without status are active
  status?: 'active' | 'draft'
  created_at: string
  updated_at: string
  // The trigger's cron expression, absent for a manual-only playbook. Lifted
  // out of the graph so a list does not have to walk the document.
  schedule_cron?: string
  // The SERVER's reading of that expression. Never recomputed here: cronexpr
  // on the backend is the implementation the scheduler itself runs on, and a
  // second one in TypeScript would be free to disagree with the thing that
  // actually fires.
  schedule_description?: string
  // Absent for an expression that is valid but can never fire ("0 0 30 2 *" —
  // February has no 30th). That saves cleanly, and the list row is the only
  // place an operator can notice it.
  next_run_at?: string
}

// Frozen copy of a template pinned to an execution when it starts
export type WorkflowTemplateSnapshot = {
  template_id: string
  name: string
  description?: string
  steps: WorkflowStep[]
  graph?: WorkflowGraph
  taken_at: string
}

// Workflow execution types
export type WorkflowExecutionStatus =
  'pending' | 'running' | 'completed' | 'failed' | 'partially_failed'

export type WorkflowExecutionNodeStatus =
  'pending' | 'running' | 'completed' | 'failed'

// Run state of one graph block on one node (keyed by graph node ID)
export type WorkflowGraphNodeState = {
  status: 'running' | 'completed' | 'failed'
  command_id?: string
  started_at?: string
  completed_at?: string
  error?: string
}

export type WorkflowExecutionNode = {
  id: string
  execution_id: string
  node_id: string
  account_id: string
  current_step_index: number
  status: WorkflowExecutionNodeStatus
  current_command_id?: string
  error_message?: string
  graph_state?: Record<string, WorkflowGraphNodeState>
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at: string
}

export type WorkflowExecution = {
  id: string
  template_id: string
  account_id: string
  status: WorkflowExecutionStatus
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at: string
  nodes?: WorkflowExecutionNode[]
  node_count?: number
  // Target node ids of the run (for OU-based filtering in lists)
  node_ids?: string[]
  template_snapshot?: WorkflowTemplateSnapshot
}

// Server-paginated executions list (newest first)
export type WorkflowExecutionListResult = {
  executions: WorkflowExecution[]
  count: number
  total: number
  page: number
  per_page: number
}

export type WorkflowExecutionListParams = {
  templateId?: string
  // Runs targeting at least one node in this OU's subtree
  ouId?: string
  page?: number
  perPage?: number
}
