import { useNavigate } from '@tanstack/react-router'
import {
  addEdge,
  Background,
  BackgroundVariant,
  Controls,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Connection,
  type Edge,
  type NodeTypes,
  type ReactFlowInstance,
} from '@xyflow/react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import '@xyflow/react/dist/style.css'

import { useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import {
  createWorkflowTemplateFx,
  deleteWorkflowTemplateFx,
  exportWorkflowTemplateFx,
  fetchWorkflowTemplatesFx,
  updateWorkflowTemplateFx,
  useWorkflowBlocks,
  useWorkflowTemplate,
} from '@/entities/workflow'
import type { WorkflowBlock } from '@/entities/workflow/api/types'
import { scopeOUIDs } from '@/entities/workflow/lib/scope'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'

import { BlockNodeView } from './blockNode'
import {
  bootWarnings,
  canConnect,
  chainTail,
  copyFragment,
  countRedactedParams,
  emptyGraph,
  flowToGraph,
  graphToFlow,
  missingParamErrors,
  newNodeId,
  parseFragment,
  pasteFragment,
  validateFlow,
  type BlockNode,
} from './graphUtils'
import { BlockInspector } from './inspector'
import { BlockPalette, PALETTE_DND_TYPE } from './palette'
import { PropertiesPanel } from './propertiesPanel'

const nodeTypes: NodeTypes = { block: BlockNodeView }

// Canonical form of everything the editor can change, used to detect unsaved
// changes: the baseline is captured from the SAME flow representation the
// canvas edits, so a load → snapshot round trip is always equal.
// Takes the scope as a SET and joins it here, so the baseline and the live
// comparison cannot key on different shapes — they did, and a multi-unit
// playbook therefore opened permanently "dirty", warning about unsaved changes
// nobody had made.
// seedParams materializes the registry defaults so the panel reflects the
// block's true defaults (Capture image's generalize=true, secret_upsert's
// sensitive=true) instead of an empty map that a checkbox renders as false —
// which silently diverges from what the backend applies.
const seedParams = (block: WorkflowBlock): Record<string, unknown> => {
  const params: Record<string, unknown> = {}
  block.params?.forEach((p) => {
    if (p.default !== undefined && p.default !== null) {
      params[p.name] = p.default
    }
  })
  return params
}

const editorSnapshot = (
  name: string,
  description: string,
  ouIds: string[],
  status: string,
  nodes: BlockNode[],
  edges: Edge[],
): string =>
  JSON.stringify({
    name: name.trim(),
    description: description.trim(),
    ouIds: [...ouIds].sort().join(','),
    status,
    graph: flowToGraph(nodes, edges),
  })

export type WorkflowEditorPageProps = {
  templateId?: string
}

export const WorkflowEditorPage = ({ templateId }: WorkflowEditorPageProps) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const isEditMode = !!templateId
  const { template, fetchTemplate } = useWorkflowTemplate(templateId || '')
  const { blocks, fetchBlocks } = useWorkflowBlocks()

  const [nodes, setNodes, onNodesChange] = useNodesState<BlockNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])
  const { ous } = useOUs()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  // Legacy flag with no UI: preserved on save so existing data keeps it
  const [isShortcut, setIsShortcut] = useState(false)
  const [ouIds, setOuIds] = useState<string[]>([])
  const [status, setStatus] = useState<'active' | 'draft'>('active')
  const [isSaving, setIsSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  // The right column shows ONE thing at a time. Selection drives it by default
  // (the block inspector); an explicit click on a palette entry or on
  // Properties takes it over until closed, and selecting a block on the canvas
  // hands it back — otherwise "I clicked a block and nothing happened" is the
  // experience.
  const [rightPanel, setRightPanel] = useState<{ kind: 'properties' } | null>(
    null,
  )
  // A block chosen in the palette and not yet placed. It is edited through the
  // SAME inspector a placed block uses, so the parameters can be filled in
  // before it lands and there is no second, read-only panel to close by hand.
  const [draftNode, setDraftNode] = useState<BlockNode | null>(null)
  const [isDeleting, setIsDeleting] = useState(false)
  const [isCloning, setIsCloning] = useState(false)
  const [confirmCloseOpen, setConfirmCloseOpen] = useState(false)
  // Transient clipboard feedback (dropped edges, unreachable entries, a
  // privileged fragment this role cannot author).
  const [pasteNotices, setPasteNotices] = useState<string[]>([])
  const loadedTemplateIdRef = useRef<string | null>(null)
  // Snapshot of the last loaded/saved state; null until the canvas is
  // initialized (nothing to lose before that).
  const savedSnapshotRef = useRef<string | null>(null)

  const canAuthorPrivileged = useCan()(Actions.PlaybookAuthorPrivileged)

  // Clipboard copy/paste of graph fragments (§4.2). The copy/paste EVENTS are
  // used rather than navigator.clipboard, which needs a secure context and
  // fails on an install served over plain HTTP. Listeners stand down inside form fields
  // and the code editor ([contenteditable] covers .cm-content), and when the
  // user has ordinary text selected.
  useEffect(() => {
    const inEditableTarget = (e: ClipboardEvent) => {
      const t = e.target as HTMLElement | null
      return !!t?.closest?.('input, select, textarea, [contenteditable]')
    }
    const onCopy = (e: ClipboardEvent) => {
      if (inEditableTarget(e) || window.getSelection()?.toString()) return
      const fragment = copyFragment(nodes, edges)
      if (!fragment || !e.clipboardData) return
      e.preventDefault()
      const text = JSON.stringify(fragment)
      e.clipboardData.setData('application/json', text)
      e.clipboardData.setData('text/plain', text)
    }
    const onPaste = (e: ClipboardEvent) => {
      if (inEditableTarget(e)) return
      const text =
        e.clipboardData?.getData('application/json') ||
        e.clipboardData?.getData('text/plain') ||
        ''
      const fragment = parseFragment(text)
      if (!fragment) return
      e.preventDefault()
      const outputsByType: Record<string, string[]> = {}
      blocks.forEach((b) => {
        outputsByType[b.type] = b.outputs.map((o) => o.name)
      })
      const result = pasteFragment(fragment, nodes, edges, outputsByType)
      if (!result) return
      const warnings = [...result.warnings]
      // Paste-time warning, not a gate: the server's authoring check is the
      // authority, but a whole-document 403 at save is a worse place to learn
      // it. `dangerous` is the exposed proxy — every dangerous block is
      // privileged (TestEveryDangerousBlockIsPrivileged).
      if (!canAuthorPrivileged) {
        const privileged = result.nodes.some(
          (n) => blocks.find((b) => b.type === n.data.blockType)?.dangerous,
        )
        if (privileged) {
          warnings.push(t('pages.workflowEditor.pastePrivilegedWarning'))
        }
      }
      setNodes((current) => [
        ...current.map((n) => ({ ...n, selected: false })),
        ...result.nodes,
      ])
      setEdges((current) => [...current, ...result.edges])
      setPasteNotices(warnings)
    }
    document.addEventListener('copy', onCopy)
    document.addEventListener('paste', onPaste)
    return () => {
      document.removeEventListener('copy', onCopy)
      document.removeEventListener('paste', onPaste)
    }
  }, [nodes, edges, blocks, canAuthorPrivileged, setNodes, setEdges, t])

  useEffect(() => {
    if (blocks.length === 0) {
      fetchBlocks()
    }
  }, [blocks.length, fetchBlocks])

  useEffect(() => {
    if (isEditMode && !template) {
      fetchTemplate()
    }
  }, [isEditMode, template, fetchTemplate])

  // Initialize the canvas once: from the template's graph in edit mode
  // (synthesized server-side for legacy templates), or a lone Start block.
  useEffect(() => {
    if (!isEditMode) {
      if (loadedTemplateIdRef.current !== 'new') {
        loadedTemplateIdRef.current = 'new'
        const initial = emptyGraph()
        setNodes(initial.nodes)
        setEdges(initial.edges)
        savedSnapshotRef.current = editorSnapshot(
          '',
          '',
          [],
          'active',
          initial.nodes,
          initial.edges,
        )
      }
      return
    }
    if (!template || loadedTemplateIdRef.current === template.id) {
      return
    }
    loadedTemplateIdRef.current = template.id
    setName(template.name)
    setDescription(template.description || '')
    setIsShortcut(template.is_shortcut || false)
    setOuIds(scopeOUIDs(template))
    setStatus(template.status === 'draft' ? 'draft' : 'active')
    const initial = template.graph ? graphToFlow(template.graph) : emptyGraph()
    setNodes(initial.nodes)
    setEdges(initial.edges)
    savedSnapshotRef.current = editorSnapshot(
      template.name,
      template.description || '',
      scopeOUIDs(template),
      template.status === 'draft' ? 'draft' : 'active',
      initial.nodes,
      initial.edges,
    )
  }, [isEditMode, template, setNodes, setEdges])

  const blocksByType = useMemo(() => {
    const map: Record<string, WorkflowBlock> = {}
    blocks.forEach((block) => {
      map[block.type] = block
    })
    return map
  }, [blocks])

  // Multi-selection is already ON — React Flow's installed defaults are
  // selectionKeyCode 'Shift', multiSelectionKeyCode Meta/Control and
  // elementsSelectable true — so Shift-box-select and Cmd-click have always
  // written `selected: true` onto several nodes at once. Reading that with
  // `.find()` picked an ARBITRARY member: the inspector edited one node of
  // several, and its Delete button removed one while Backspace removed all.
  const selectedNodes = useMemo(
    () => nodes.filter((node) => node.selected),
    [nodes],
  )
  // The inspector edits ONE block. With several selected it would silently
  // apply to whichever came first, so it steps aside for a selection summary.
  const selectedNode = selectedNodes.length === 1 ? selectedNodes[0] : undefined
  // Structural problems always block saving; unfilled required params only
  // block an ACTIVE playbook — a draft may be saved half-built (the backend
  // applies the same relaxation, and drafts cannot run).
  const structuralErrors = useMemo(
    () => validateFlow(nodes, edges, blocksByType),
    [nodes, edges, blocksByType],
  )
  const paramErrors = useMemo(
    () => missingParamErrors(nodes, blocksByType),
    [nodes, blocksByType],
  )
  // Boot-state warnings never enter validationErrors: they must not gate
  // saving. A block can be legitimately placed where this walk cannot follow —
  // a run started on an already-netbooted node is the ordinary install flow —
  // so a refusal here would be wrong more often than right.
  const bootIssues = useMemo(
    () => bootWarnings(nodes, edges, blocksByType),
    [nodes, edges, blocksByType],
  )

  const validationErrors = useMemo(
    () =>
      status === 'draft'
        ? structuralErrors
        : [...structuralErrors, ...paramErrors],
    [status, structuralErrors, paramErrors],
  )

  const deletableSelectionCount = selectedNodes.filter(
    (node) => node.deletable !== false,
  ).length

  const isDirty = useMemo(() => {
    if (savedSnapshotRef.current === null) return false
    return (
      editorSnapshot(name, description, ouIds, status, nodes, edges) !==
      savedSnapshotRef.current
    )
  }, [name, description, ouIds, status, nodes, edges])

  const handleConnect = useCallback(
    (connection: Connection) => {
      if (!connection.source || !connection.target) return
      if (
        !canConnect(
          connection.source,
          connection.sourceHandle ?? null,
          connection.target,
          nodes,
          edges,
        )
      ) {
        return
      }
      const port = connection.sourceHandle || 'out'
      setEdges((current) =>
        addEdge(
          {
            ...connection,
            label: port !== 'out' ? port : undefined,
          },
          current,
        ),
      )
    },
    [nodes, edges, setEdges],
  )

  const handleAddBlock = useCallback(
    (
      block: WorkflowBlock,
      dropAt?: { x: number; y: number },
      data?: BlockNode['data'],
    ) => {
      const tail = chainTail(nodes, edges)
      // A dropped block lands where it was dropped; a clicked one continues the
      // chain, which is what "Add to canvas" means with no pointer involved.
      const position =
        dropAt ??
        (tail
          ? { x: tail.position.x, y: tail.position.y + 160 }
          : { x: 120, y: 60 + nodes.length * 160 })
      const node: BlockNode = {
        id: newNodeId(),
        type: 'block',
        position,
        data: data ?? { blockType: block.type, params: seedParams(block) },
      }
      setNodes((current) =>
        current
          .map((n) => ({ ...n, selected: false }))
          .concat({
            ...node,
            selected: true,
          }),
      )
      // Auto-connect to a single linear tail; branch points are wired by hand.
      //
      // A dropped block is wired too. Leaving it loose was the earlier choice
      // and it was wrong on more than taste: reachability from the trigger is
      // checked by the same validate() every save goes through, so an
      // unconnected block made the whole document UNSAVEABLE until the author
      // noticed and drew the edge. Landing where it was dropped is the part of
      // the gesture worth keeping; being disconnected is not.
      if (tail) {
        setEdges((current) =>
          addEdge(
            {
              source: tail.id,
              target: node.id,
              sourceHandle: 'out',
              targetHandle: null,
            },
            current,
          ),
        )
      }
    },
    [nodes, edges, setNodes, setEdges],
  )

  // Drag-and-drop from the palette. The canvas wrapper converts the pointer
  // position into flow coordinates itself rather than through useReactFlow,
  // which would need a provider this page does not mount.
  const flowWrapperRef = useRef<HTMLDivElement | null>(null)
  const reactFlowInstance = useRef<ReactFlowInstance<BlockNode, Edge> | null>(
    null,
  )

  const handleCanvasDrop = useCallback(
    (event: React.DragEvent<HTMLDivElement>) => {
      const blockType = event.dataTransfer.getData(PALETTE_DND_TYPE)
      if (!blockType) return
      event.preventDefault()
      const block = blocks.find((b) => b.type === blockType)
      if (!block) return
      const bounds = flowWrapperRef.current?.getBoundingClientRect()
      const viewport = reactFlowInstance.current?.getViewport()
      if (!bounds || !viewport) {
        handleAddBlock(block)
        setDraftNode(null)
        setRightPanel(null)
        return
      }
      handleAddBlock(block, {
        x: (event.clientX - bounds.left - viewport.x) / viewport.zoom,
        y: (event.clientY - bounds.top - viewport.y) / viewport.zoom,
      })
      // Hand the right column back to the block that was just placed: the
      // draft panel answered "what is this", and the question after dropping is
      // "what do I set on it". Leaving it up would also offer to add the same
      // block a second time.
      setDraftNode(null)
      setRightPanel(null)
    },
    [blocks, handleAddBlock],
  )

  const handleCanvasDragOver = useCallback(
    (event: React.DragEvent<HTMLDivElement>) => {
      if (!event.dataTransfer.types.includes(PALETTE_DND_TYPE)) return
      event.preventDefault()
      event.dataTransfer.dropEffect = 'copy'
    },
    [],
  )

  const handleInspectorChange = useCallback(
    (data: Partial<BlockNode['data']>) => {
      if (!selectedNode) return
      setNodes((current) =>
        current.map((node) =>
          node.id === selectedNode.id
            ? { ...node, data: { ...node.data, ...data } }
            : node,
        ),
      )
    },
    [selectedNode, setNodes],
  )

  // Deletes the WHOLE selection, and honours `deletable: false` on the
  // trigger. React Flow's own Backspace path respects that flag; a hand-rolled
  // filter does not, and dropping the trigger makes the document unsaveable
  // (every graph needs exactly one).
  const handleDeleteSelected = useCallback(() => {
    const removable = new Set(
      selectedNodes.filter((node) => node.deletable !== false).map((n) => n.id),
    )
    if (removable.size === 0) return
    setNodes((current) => current.filter((node) => !removable.has(node.id)))
    setEdges((current) =>
      current.filter(
        (edge) => !removable.has(edge.source) && !removable.has(edge.target),
      ),
    )
  }, [selectedNodes, setNodes, setEdges])

  // Unconditional navigation back to the list — used after save/delete and
  // by the discard confirmation.
  const handleClose = useCallback(() => {
    navigate({
      to: '/workflows',
      search: {
        sidebar: undefined,
        executionId: undefined,
        templateId: undefined,
        ou: undefined,
        tab: 'templates',
      },
    })
  }, [navigate])

  // Cancel / the back arrow go through here: unsaved changes must be
  // acknowledged before they are thrown away.
  const requestClose = useCallback(() => {
    if (isDirty) {
      setConfirmCloseOpen(true)
      return
    }
    handleClose()
  }, [isDirty, handleClose])

  const handleSave = useCallback(async () => {
    setSaveError(null)
    if (!name.trim()) {
      setSaveError(t('pages.workflowEditor.nameRequired'))
      return
    }
    if (validationErrors.length > 0) {
      return
    }
    // A field showing the redaction placeholder is a value the server HID
    // because this role lacks secret.read_value. Saving it back would
    // overwrite the real script/header/url with the placeholder text — the
    // server refuses it too; refusing here keeps the draft in the editor.
    {
      const redactedFields = countRedactedParams(nodes)
      if (redactedFields > 0) {
        setSaveError(
          t('pages.workflowEditor.redactedError', { count: redactedFields }),
        )
        return
      }
    }
    setIsSaving(true)
    try {
      const payload = {
        name: name.trim(),
        description: description.trim(),
        is_shortcut: isShortcut,
        ou_ids: ouIds,
        status,
        steps: [],
        graph: flowToGraph(nodes, edges),
        // Compare-and-swap against what this editor loaded. Without it this
        // full-document save writes every field back and silently reverts
        // whatever another operator saved in the meantime; with it the server
        // answers 409 and the message below tells the user to reload.
        expected_updated_at: template?.updated_at,
      }
      if (isEditMode && templateId) {
        await updateWorkflowTemplateFx({ templateId, payload })
      } else {
        await createWorkflowTemplateFx(payload)
      }
      await fetchWorkflowTemplatesFx()
      handleClose()
    } catch (error: unknown) {
      const err = error as {
        response?: {
          status?: number
          data?: { message?: string; error?: string }
        }
        message?: string
      }
      // 409 is the compare-and-swap refusing to overwrite someone else's save.
      // Say what happened and what to do about it: the raw server text names
      // two timestamps, which tells a user nothing actionable.
      if (err?.response?.status === 409) {
        setSaveError(t('pages.workflowEditor.conflictError'))
        return
      }
      setSaveError(
        err?.response?.data?.message ||
          err?.response?.data?.error ||
          err?.message ||
          t('pages.workflowEditor.saveError'),
      )
    } finally {
      setIsSaving(false)
    }
  }, [
    name,
    description,
    isShortcut,
    ouIds,
    status,
    nodes,
    edges,
    validationErrors,
    isEditMode,
    templateId,
    handleClose,
    t,
  ])

  // Duplicate the CURRENT canvas state (including unsaved edits) as a draft
  // copy and switch the editor to it — the original stays as saved.
  const handleClone = useCallback(async () => {
    setSaveError(null)
    // The copy is created as a draft, so only structural problems block it.
    if (structuralErrors.length > 0) return
    setIsCloning(true)
    try {
      const copy = await createWorkflowTemplateFx({
        name: t('pages.workflowEditor.cloneName', {
          name: name.trim() || t('pages.workflowEditor.cloneDefaultName'),
        }),
        description: description.trim(),
        is_shortcut: isShortcut,
        ou_ids: ouIds,
        status: 'draft',
        steps: [],
        graph: flowToGraph(nodes, edges),
      })
      navigate({
        to: '/workflows/$templateId/edit',
        params: { templateId: copy.id },
        search: { sidebar: undefined },
      })
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } } }
      setSaveError(
        err?.response?.data?.message || t('pages.workflowEditor.cloneError'),
      )
    } finally {
      setIsCloning(false)
    }
  }, [
    name,
    description,
    isShortcut,
    ouIds,
    nodes,
    edges,
    structuralErrors,
    navigate,
    t,
  ])

  // Exports the last SAVED version — unsaved canvas edits are not included.
  const handleExport = useCallback(() => {
    if (!templateId) return
    exportWorkflowTemplateFx(templateId)
  }, [templateId])

  const handleConfirmDelete = useCallback(async () => {
    if (!templateId) return
    setIsDeleting(true)
    try {
      await deleteWorkflowTemplateFx(templateId)
      setDeleteOpen(false)
      handleClose()
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } } }
      setDeleteOpen(false)
      setSaveError(
        err?.response?.data?.message || t('pages.workflowEditor.deleteError'),
      )
    } finally {
      setIsDeleting(false)
    }
  }, [templateId, handleClose, t])

  if (isEditMode && !template) {
    return (
      <div className="h-full w-full flex items-center justify-center text-neutral-4">
        {t('pages.workflowEditor.loading')}
      </div>
    )
  }

  const canSave = !isSaving && !!name.trim() && validationErrors.length === 0

  return (
    <div className="h-full w-full flex flex-col">
      {/* Top bar */}
      <div className="flex items-center gap-4 px-4 py-3 border-b border-neutral-11">
        <button
          type="button"
          onClick={requestClose}
          className="text-neutral-7 hover:text-neutral-5 text-sm shrink-0"
        >
          {t('pages.workflowEditor.back')}
        </button>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          className="px-3 py-1.5 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5 w-72"
          placeholder={t('pages.workflowEditor.namePlaceholder')}
          disabled={isSaving}
        />
        <button
          type="button"
          onClick={() =>
            setRightPanel((current) =>
              current?.kind === 'properties' ? null : { kind: 'properties' },
            )
          }
          className={
            rightPanel?.kind === 'properties'
              ? 'px-2.5 py-1.5 text-xs uppercase rounded border border-purple-5 text-neutral-1 shrink-0'
              : 'px-2.5 py-1.5 text-xs uppercase rounded border border-neutral-11 text-neutral-5 hover:text-neutral-2 transition-colors shrink-0'
          }
          title={t('pages.workflowEditor.propertiesTooltip')}
        >
          {t('pages.workflowEditor.propertiesButton')}
        </button>
        {/* An unlabelled two-button segment was the whole explanation of a
            state that decides whether the playbook can EVER be run: drafts are
            filtered out of every run picker and never fire on a schedule, and
            every creation path (import, starter, clone) forces one. Say what
            the control is and what the choice costs. */}
        <div className="flex items-center gap-2 shrink-0">
          <span className="text-xs uppercase text-neutral-7">
            {t('pages.workflowEditor.status')}
          </span>
          <div className="flex rounded border border-neutral-11 overflow-hidden">
            {(['active', 'draft'] as const).map((value) => (
              <button
                key={value}
                type="button"
                onClick={() => setStatus(value)}
                disabled={isSaving}
                title={
                  value === 'draft'
                    ? t('pages.workflowEditor.statusDraftTooltip')
                    : t('pages.workflowEditor.statusActiveTooltip')
                }
                className={
                  status === value
                    ? 'px-2.5 py-1.5 text-xs uppercase bg-neutral-12 text-lime-6'
                    : 'px-2.5 py-1.5 text-xs uppercase text-neutral-5 hover:text-neutral-2 transition-colors'
                }
              >
                {value === 'draft'
                  ? t('pages.workflowEditor.statusDraft')
                  : t('pages.workflowEditor.statusActive')}
              </button>
            ))}
          </div>
          {status === 'draft' && (
            <span className="text-xs text-yellow-5 whitespace-nowrap">
              {t('pages.workflowEditor.draftBadge')}
            </span>
          )}
        </div>
        <div className="flex-1 min-w-0 text-xs truncate">
          {saveError || validationErrors.length > 0 ? (
            <span className="text-red-6">
              {saveError ||
                `${validationErrors[0]}${
                  validationErrors.length > 1
                    ? t('pages.workflowEditor.moreSuffix', {
                        count: validationErrors.length - 1,
                      })
                    : ''
                }`}
            </span>
          ) : pasteNotices.length > 0 ? (
            <span
              className="cursor-pointer text-yellow-5"
              title={`${pasteNotices.join('\n')}\n${t(
                'pages.workflowEditor.pasteDismissHint',
              )}`}
              onClick={() => setPasteNotices([])}
            >
              {`${pasteNotices[0]}${
                pasteNotices.length > 1
                  ? t('pages.workflowEditor.moreSuffix', {
                      count: pasteNotices.length - 1,
                    })
                  : ''
              }`}
            </span>
          ) : status === 'draft' && paramErrors.length > 0 ? (
            // A draft may be saved with unfilled required params; keep them
            // visible so activation doesn't fail as a surprise.
            <span className="text-yellow-5">
              {t('pages.workflowEditor.draftParamHint', {
                message: `${paramErrors[0]}${
                  paramErrors.length > 1
                    ? t('pages.workflowEditor.moreSuffix', {
                        count: paramErrors.length - 1,
                      })
                    : ''
                }`,
              })}
            </span>
          ) : (
            // The slot used to be reachable only for drafts, so an ACTIVE
            // playbook had nowhere to show a non-blocking message at all.
            bootIssues.length > 0 && (
              <span className="text-yellow-5" title={bootIssues.join('\n')}>
                {`${bootIssues[0]}${
                  bootIssues.length > 1
                    ? t('pages.workflowEditor.moreSuffix', {
                        count: bootIssues.length - 1,
                      })
                    : ''
                }`}
              </span>
            )
          )}
        </div>
        {isEditMode && (
          <div className="flex items-center gap-1 shrink-0 pr-2">
            <Button
              size="md"
              kind="tertiary"
              onClick={handleClone}
              disabled={isCloning || isSaving || structuralErrors.length > 0}
              title={t('pages.workflowEditor.cloneTooltip')}
            >
              {isCloning
                ? t('pages.workflowEditor.cloning')
                : t('pages.workflowEditor.clone')}
            </Button>
            <Button
              size="md"
              kind="tertiary"
              onClick={handleExport}
              disabled={isSaving}
              title={t('pages.workflowEditor.exportTooltip')}
            >
              {t('pages.workflowEditor.export')}
            </Button>
            <Button
              size="md"
              kind="tertiary"
              variant="destructive"
              onClick={() => setDeleteOpen(true)}
              disabled={isSaving || isDeleting}
              title={t('pages.workflowEditor.deleteTooltip')}
            >
              {t('pages.workflowEditor.delete')}
            </Button>
          </div>
        )}
        <Button
          size="md"
          kind="tertiary"
          onClick={requestClose}
          disabled={isSaving}
        >
          {t('pages.workflowEditor.cancel')}
        </Button>
        <Button size="md" onClick={handleSave} disabled={!canSave}>
          {isSaving
            ? t('pages.workflowEditor.saving')
            : isEditMode
              ? t('pages.workflowEditor.save')
              : t('pages.workflowEditor.create')}
        </Button>
      </div>

      <ConfirmDialog
        open={deleteOpen}
        title={t('pages.workflowEditor.deleteTitle')}
        message={t('pages.workflowEditor.deleteMessage', {
          name: name || template?.name,
        })}
        confirmText={t('pages.workflowEditor.deleteConfirm')}
        destructive
        busy={isDeleting}
        onConfirm={handleConfirmDelete}
        onCancel={() => setDeleteOpen(false)}
      />
      <ConfirmDialog
        open={confirmCloseOpen}
        title={t('pages.workflowEditor.discardTitle')}
        message={t('pages.workflowEditor.discardMessage')}
        confirmText={t('pages.workflowEditor.discardConfirm')}
        destructive
        onConfirm={() => {
          setConfirmCloseOpen(false)
          handleClose()
        }}
        onCancel={() => setConfirmCloseOpen(false)}
      />

      {/* Body: palette | canvas | inspector */}
      <div className="flex-1 flex min-h-0">
        <BlockPalette
          blocks={blocks}
          onPreviewBlock={(block) => {
            setRightPanel(null)
            setDraftNode({
              id: 'draft',
              type: 'block',
              position: { x: 0, y: 0 },
              data: { blockType: block.type, params: seedParams(block) },
            })
          }}
          previewedType={draftNode?.data.blockType}
        />
        <div
          className="flex-1 min-w-0"
          ref={flowWrapperRef}
          onDrop={handleCanvasDrop}
          onDragOver={handleCanvasDragOver}
        >
          <ReactFlow
            onInit={(instance) => {
              reactFlowInstance.current = instance
            }}
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={handleConnect}
            // Clicking a placed block asks for THAT block's panel. Without
            // this the draft kept the column and the click looked ignored.
            onNodeClick={() => {
              setDraftNode(null)
              setRightPanel(null)
            }}
            colorMode="dark"
            // Fixed top-left viewport instead of fitView: fitView centers the
            // content, so a fresh playbook opened with START floating mid-canvas.
            // Graph layouts start at ~(120,60), so 1:1 from the origin puts the
            // chain at the top-left where editing begins.
            defaultViewport={{ x: 0, y: 0, zoom: 1 }}
            proOptions={{ hideAttribution: true }}
            defaultEdgeOptions={{ animated: false }}
            className="bg-neutral-14"
          >
            <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>
        {rightPanel?.kind === 'properties' && (
          <PropertiesPanel
            name={name}
            description={description}
            ouIds={ouIds}
            ous={ous}
            disabled={isSaving}
            onNameChange={setName}
            onDescriptionChange={setDescription}
            onClose={() => setRightPanel(null)}
          />
        )}
        {!rightPanel && draftNode && (
          <BlockInspector
            key={draftNode.data.blockType}
            node={draftNode}
            block={blocksByType[draftNode.data.blockType]}
            mode="draft"
            onChange={(data) =>
              setDraftNode((current) =>
                current
                  ? { ...current, data: { ...current.data, ...data } }
                  : current,
              )
            }
            onDelete={() => setDraftNode(null)}
            onAdd={() => {
              const block = blocksByType[draftNode.data.blockType]
              if (block) handleAddBlock(block, undefined, draftNode.data)
              setDraftNode(null)
            }}
          />
        )}
        {!rightPanel && !draftNode && selectedNode && (
          <BlockInspector
            key={selectedNode.id}
            node={selectedNode}
            block={blocksByType[selectedNode.data.blockType]}
            onChange={handleInspectorChange}
            onDelete={handleDeleteSelected}
          />
        )}
        {!rightPanel && !draftNode && selectedNodes.length > 1 && (
          <div className="w-80 shrink-0 border-l border-neutral-11 p-4 flex flex-col gap-3">
            <div className="text-sm font-bold uppercase text-neutral-1">
              {t('pages.workflowEditor.selectedCount', {
                count: selectedNodes.length,
              })}
            </div>
            <div className="text-xs text-neutral-6">
              {t('pages.workflowEditor.multiSelectHint')}
            </div>
            <button
              type="button"
              onClick={handleDeleteSelected}
              disabled={deletableSelectionCount === 0}
              className="self-start text-xs px-2 py-1 rounded border border-red-6/40 text-red-6 hover:bg-red-1/10 disabled:opacity-50 transition-colors"
            >
              {t('pages.workflowEditor.deleteSelected', {
                count: deletableSelectionCount,
              })}
            </button>
            {deletableSelectionCount < selectedNodes.length && (
              <div className="text-xs text-neutral-6">
                {t('pages.workflowEditor.triggerNotDeletable')}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
