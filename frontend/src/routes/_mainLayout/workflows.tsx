import { createFileRoute, useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { t as tGlobal } from 'i18next'
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
} from 'react'
import { useTranslation } from 'react-i18next'

import { OperationsImagesTab } from '@/pages/operations/imagesTab'

import { OU_FILTER_ALL } from '@/widgets/ouRail'
import { OURailAside } from '@/widgets/ouRail/OURailAside.tsx'
import { SidebarEnum } from '@/widgets/sidebars/types.ts'

import { useNodes } from '@/entities/node/hooks.ts'
import { imagesModel } from '@/entities/osImage'
import type { ImageKind } from '@/entities/osImage/api/types'
import { useImages } from '@/entities/osImage/hooks'
import { IMAGE_DND_TYPE } from '@/entities/osImage/ui/imageList/imageRow'
import { ousModel, useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import {
  createWorkflowTemplateFx,
  deleteWorkflowTemplateFx,
  exportWorkflowTemplateFx,
  fetchWorkflowExecutionsFx,
  fetchWorkflowTemplatesFx,
  importWorkflowTemplateFx,
  isWorkflowAvailableForOU,
  scopeOUIDs,
  setWorkflowTemplateOUsFx,
  useWorkflowExecutions,
  useWorkflowStarters,
  useWorkflowTemplates,
} from '@/entities/workflow'
import type {
  PortablePlaybookDocument,
  WorkflowExecution,
  WorkflowStarter,
  WorkflowTemplate,
} from '@/entities/workflow/api/types'
import { RunPlaybookDialog } from '@/entities/workflow/ui/runPlaybookDialog'
import { StarterPickerDialog } from '@/entities/workflow/ui/starterPickerDialog'
import { WorkflowExecutionList } from '@/entities/workflow/ui/workflowExecutionList'
import {
  WORKFLOW_DND_TYPE,
  WorkflowTable,
} from '@/entities/workflow/ui/workflowTable'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError } from '@/shared/ui/toast'

// The Operations section: playbooks and runs, plus the OS image inventory
// split by kind (docs/ORGANIZATION-PAGE-PLAN.md, companion change). The API
// and route path keep the historical workflow naming.
type TabType =
  'templates' | 'runs' | 'install-images' | 'backups' | 'gold-images'
type StatusFilter = 'all' | 'active' | 'draft'

const TAB_VALUES: TabType[] = [
  'templates',
  'runs',
  'install-images',
  'backups',
  'gold-images',
]
// A FUNCTION, not a const record: the labels come from i18next, which is not
// initialised when this module is first evaluated (same reasoning as
// entities/node/ui/nodeList/nodeColumns.tsx).
const tabLabels = (): Record<TabType, string> => ({
  templates: tGlobal('routes.workflows.tabs.templates'),
  runs: tGlobal('routes.workflows.tabs.runs'),
  'install-images': tGlobal('routes.workflows.tabs.installImages'),
  backups: tGlobal('routes.workflows.tabs.backups'),
  'gold-images': tGlobal('routes.workflows.tabs.goldImages'),
})
const IMAGE_TAB_KIND: Partial<Record<TabType, ImageKind>> = {
  'install-images': 'os',
  backups: 'backup',
  'gold-images': 'golden',
}

// Outcome of importing a portable playbook (file upload), shown in a dialog:
// import warnings are a list the operator has to read and decide about, which
// is a modal, not a toast.
type ImportOutcome =
  | { kind: 'success'; template: WorkflowTemplate; warnings: string[] }
  | { kind: 'error'; message: string }

const importErrorMessage = (err: unknown): string => {
  const axiosMessage = (err as { response?: { data?: { message?: string } } })
    ?.response?.data?.message
  if (axiosMessage) return axiosMessage
  if (err instanceof SyntaxError)
    return tGlobal('routes.workflows.importErrorNotJson')
  return tGlobal('routes.workflows.importErrorGeneric')
}

// Runs pager (server-side pagination; page size persisted like the secrets list)
const RUNS_PAGE_SIZE_KEY = 'workflow-runs-page-size'
const RUNS_PAGE_SIZES = [25, 50, 100]
const readRunsPageSize = () => {
  const stored = Number(localStorage.getItem(RUNS_PAGE_SIZE_KEY))
  return RUNS_PAGE_SIZES.includes(stored) ? stored : 50
}

export const Route = createFileRoute('/_mainLayout/workflows')({
  validateSearch: (search: Record<string, unknown>) => {
    const sidebar = search.sidebar
    const templateId = search.templateId
    const executionId = search.executionId
    const tab = search.tab
    const ou = search.ou
    const base = {
      templateId: typeof templateId === 'string' ? templateId : undefined,
      ou: typeof ou === 'string' ? ou : undefined,
      tab: (typeof tab === 'string' && TAB_VALUES.includes(tab as TabType)
        ? tab
        : 'templates') as TabType,
    }
    if (
      sidebar === 'workflowExecutionDetails' &&
      typeof executionId === 'string'
    ) {
      return {
        ...base,
        sidebar: 'workflowExecutionDetails' as const,
        executionId,
        tab: 'runs' as TabType,
      }
    }
    if (sidebar === 'osImageDetails' && typeof search.imageId === 'string') {
      return {
        ...base,
        sidebar: 'osImageDetails' as const,
        imageId: search.imageId,
      }
    }
    if (sidebar === 'uploadOSImage') {
      return { ...base, sidebar: 'uploadOSImage' as const }
    }
    return { ...base, sidebar: undefined, executionId: undefined }
  },
  component: () => {
    const navigate = useNavigate()
    const search = Route.useSearch()
    const [activeTab, setActiveTab] = useState<TabType>(
      search.tab || 'templates',
    )
    const [searchText, setSearchText] = useState('')
    const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
    const [deleteTarget, setDeleteTarget] = useState<WorkflowTemplate | null>(
      null,
    )
    const [isDeleting, setIsDeleting] = useState(false)
    const [starterPickerOpen, setStarterPickerOpen] = useState(false)
    const [starterBusy, setStarterBusy] = useState(false)
    const [starterError, setStarterError] = useState<string | undefined>()
    const [importOutcome, setImportOutcome] = useState<ImportOutcome | null>(
      null,
    )
    const importInputRef = useRef<HTMLInputElement>(null)
    const {
      starters,
      loading: startersLoading,
      fetchStarters,
    } = useWorkflowStarters()
    const { templates } = useWorkflowTemplates()
    const {
      executions,
      total: executionsTotal,
      loading: executionsLoading,
    } = useWorkflowExecutions()
    const { ous } = useOUs()
    const { nodes } = useNodes()
    const { t } = useTranslation()
    const { images } = useImages()
    // Authoring gate. This page had no permission check of any
    // kind: import, create-from-starter, create, clone, export and delete were
    // all rendered for everyone, and a viewer's click produced a 403 the UI
    // could only show as a toast after the fact. The backend has always gated
    // these — the gap was that the product invited the click.
    //
    // One action for all six. They differ in destructiveness but not in the
    // right they need: each of them writes, reads out, or removes a playbook
    // DEFINITION, which is exactly what playbook.author governs. Running one is
    // a different action and is gated where runs are started.
    const canAuthor = useCan()(Actions.PlaybookAuthor)
    // Running is its own right, and deliberately not the authoring one: a
    // viewer who may run a playbook should get the Run action even where every
    // editing affordance is absent.
    const canRun = useCan()(Actions.PlaybookRun)
    // Setting a unit's default install image is image.manage, the same right the
    // Install images tab's own actions take.
    const canManageImages = useCan()(Actions.ImageManage)
    const [runTarget, setRunTarget] = useState<WorkflowTemplate | null>(null)
    const [runsPage, setRunsPage] = useState(1)
    const [runsPageSize, setRunsPageSize] = useState(readRunsPageSize)

    const selectedOU = search.ou || OU_FILTER_ALL

    useEffect(() => {
      if (search.tab && search.tab !== activeTab) {
        setActiveTab(search.tab)
      }
    }, [search.tab, activeTab])

    // Filters changed → back to the first page (also refetches via the effect
    // below when the page was already 1).
    useEffect(() => {
      setRunsPage(1)
    }, [search.templateId, selectedOU])

    useEffect(() => {
      if (activeTab === 'runs') {
        fetchWorkflowExecutionsFx({
          templateId: search.templateId,
          ouId: selectedOU === OU_FILTER_ALL ? undefined : selectedOU,
          page: runsPage,
          perPage: runsPageSize,
        })
      }
    }, [activeTab, search.templateId, selectedOU, runsPage, runsPageSize])

    // The total can shrink under us (the hourly retention sweep deletes old
    // runs); clamp back to the last page instead of showing an empty page
    // with a "No runs yet" state while total > 0.
    useEffect(() => {
      if (executionsLoading) return
      const totalPages = Math.max(1, Math.ceil(executionsTotal / runsPageSize))
      if (runsPage > totalPages) {
        setRunsPage(totalPages)
      }
    }, [executionsLoading, executionsTotal, runsPage, runsPageSize])

    const handleTabChange = useCallback(
      (tab: TabType) => {
        setActiveTab(tab)
        navigate({
          to: '.',
          search: (old) => ({
            ...old,
            tab,
            sidebar: undefined,
            executionId: undefined,
            imageId: undefined,
          }),
        })
      },
      [navigate],
    )

    const imageKind = IMAGE_TAB_KIND[activeTab]
    const kindImagesCount = useMemo(
      () =>
        imageKind
          ? images.filter((img) => (img.kind ?? 'os') === imageKind).length
          : 0,
      [images, imageKind],
    )

    // Drop an image onto an OU → set it as that unit's default install image
    // (install-images tab only).
    const handleDropImage = useCallback(
      async (imageId: string, ouId: string | null) => {
        if (!ouId) return
        try {
          await ousModel.updateOUFx({ ouId, data: { os_image_id: imageId } })
        } catch (error) {
          showError(t('routes.workflows.setDefaultImageError'), error)
        }
      },
      [],
    )

    const handleOUSelect = useCallback(
      (value: string) => {
        navigate({
          to: '.',
          search: (old) => ({
            ...old,
            ou: value === OU_FILTER_ALL ? undefined : value,
          }),
        })
      },
      [navigate],
    )

    const handleTemplateFilterChange = useCallback(
      (templateId: string | undefined) => {
        navigate({
          to: '.',
          search: (old) => ({
            ...old,
            templateId: templateId || undefined,
            tab: 'runs',
          }),
        })
      },
      [navigate],
    )

    const handleCreateClick = useCallback(() => {
      navigate({ to: '/workflows/new', search: { sidebar: undefined } })
    }, [navigate])

    const handleEdit = useCallback(
      (templateId: string) => {
        navigate({
          to: '/workflows/$templateId/edit',
          params: { templateId },
          search: { sidebar: undefined },
        })
      },
      [navigate],
    )

    const handleConfirmDelete = useCallback(async () => {
      if (!deleteTarget) return
      setIsDeleting(true)
      try {
        await deleteWorkflowTemplateFx(deleteTarget.id)
        setDeleteTarget(null)
      } finally {
        setIsDeleting(false)
      }
    }, [deleteTarget])

    const handleExecutionClick = useCallback(
      (executionId: string) => {
        navigate({
          to: '.',
          search: {
            sidebar: SidebarEnum.WorkflowExecutionDetails,
            executionId,
            tab: 'runs',
          },
        })
      },
      [navigate],
    )

    const scopeMatches = useCallback(
      (template: WorkflowTemplate, ouId: string) =>
        isWorkflowAvailableForOU(template, ouId, ous),
      [ous],
    )

    const nodeOUById = useMemo(() => {
      const map: Record<string, string | undefined> = {}
      nodes.forEach((node) => {
        map[node.id] = node.ou_id || undefined
      })
      return map
    }, [nodes])

    // A capture belongs to the unit of its SOURCE node — that is how the rail
    // filters and counts backups and gold images.
    const captureCountsByOU = useMemo(() => {
      const counts: Record<string, number> = {}
      if (imageKind !== 'backup' && imageKind !== 'golden') return counts
      for (const img of images) {
        if ((img.kind ?? 'os') !== imageKind || !img.source_node_id) continue
        const ouId = nodeOUById[img.source_node_id]
        if (ouId) counts[ouId] = (counts[ouId] ?? 0) + 1
      }
      return counts
    }, [images, imageKind, nodeOUById])

    // A run belongs to an OU filter when at least one of its target nodes is
    // in the OU's subtree — independent of the playbook's scope. Runs whose
    // nodes were deleted only show under "All runs".
    const runMatchesOU = useCallback(
      (execution: WorkflowExecution, ouId: string) => {
        if (ouId === OU_FILTER_ALL) return true
        const subtree = new Set([ouId])
        ous.forEach((ou) => {
          if (ou.parent_id === ouId) subtree.add(ou.id)
        })
        return (execution.node_ids || []).some((nodeId) => {
          const nodeOU = nodeOUById[nodeId]
          return !!nodeOU && subtree.has(nodeOU)
        })
      },
      [ous, nodeOUById],
    )

    // Clone a playbook as a draft copy: same graph/scope, new name, hidden
    // from run pickers until activated.
    //
    // Draft is deliberate here and not an oversight worth "fixing": a copy that
    // was immediately runnable would put a second playbook with the same shape
    // in front of every operator, and a scheduled original would suddenly have
    // a twin firing beside it. What was missing is that nothing SAID so — the
    // status filter defaults to all, the row shows the status, and the run
    // pickers now name drafts as the reason they found nothing.
    const handleClone = useCallback(async (template: WorkflowTemplate) => {
      await createWorkflowTemplateFx({
        name: t('routes.workflows.cloneName', { name: template.name }),
        description: template.description || '',
        is_shortcut: template.is_shortcut || false,
        ou_ids: scopeOUIDs(template),
        status: 'draft',
        steps: template.graph ? [] : template.steps,
        graph: template.graph,
      })
    }, [])

    const handleExport = useCallback((template: WorkflowTemplate) => {
      exportWorkflowTemplateFx(template.id)
    }, [])

    // Import scope follows the OU filter: importing while a unit is selected
    // scopes the draft there, "All playbooks" imports unscoped.
    const importScopeOU = selectedOU === OU_FILTER_ALL ? null : selectedOU

    const handleImportFile = useCallback(
      async (event: ChangeEvent<HTMLInputElement>) => {
        const file = event.target.files?.[0]
        // Allow re-picking the same file after a failed attempt.
        event.target.value = ''
        if (!file) return
        try {
          const document = JSON.parse(
            await file.text(),
          ) as PortablePlaybookDocument
          const result = await importWorkflowTemplateFx({
            document,
            ou_ids: importScopeOU ? [importScopeOU] : [],
          })
          setImportOutcome({ kind: 'success', ...result })
        } catch (err) {
          setImportOutcome({ kind: 'error', message: importErrorMessage(err) })
        }
      },
      [importScopeOU],
    )

    const handleOpenStarterPicker = useCallback(() => {
      setStarterError(undefined)
      setStarterPickerOpen(true)
      fetchStarters()
    }, [fetchStarters])

    // Instantiate a starter: import its document as a draft and jump straight
    // into the editor to review it.
    const handleSelectStarter = useCallback(
      async (starter: WorkflowStarter) => {
        setStarterBusy(true)
        setStarterError(undefined)
        try {
          const result = await importWorkflowTemplateFx({
            document: starter.document,
            ou_ids: importScopeOU ? [importScopeOU] : [],
          })
          setStarterPickerOpen(false)
          navigate({
            to: '/workflows/$templateId/edit',
            params: { templateId: result.template.id },
            search: { sidebar: undefined },
          })
        } catch (err) {
          setStarterError(importErrorMessage(err))
        } finally {
          setStarterBusy(false)
        }
      },
      [importScopeOU, navigate],
    )

    // Drag a playbook row onto an OU to scope it there; the "All" row clears
    // the scope.
    //
    // This used to re-send the whole document from the row in this list, which
    // meant a drag silently reverted every field another operator had saved
    // since the page loaded — including, on a cron playbook, the scoping itself.
    // The re-target endpoint writes the one column.
    // Dropping onto a unit ADDS it to the playbook's scope rather than
    // replacing it — a playbook routinely belongs to several units, and a drop
    // that silently detached the others would make the second assignment undo
    // the first. Dropping onto "All playbooks" clears the scope, which is the
    // only gesture that can mean "available everywhere".
    const handleDropWorkflow = useCallback(
      async (templateId: string, ouId: string | null) => {
        try {
          await setWorkflowTemplateOUsFx({
            templateId,
            next: (current) => {
              if (ouId === null) return current.length === 0 ? null : []
              // Dropping onto a unit the playbook is ALREADY in removes it:
              // the gesture is a toggle, because otherwise the rail is a
              // one-way ratchet — every drop widens the scope and nothing
              // narrows it.
              return current.includes(ouId)
                ? current.filter((id) => id !== ouId)
                : [...current, ouId]
            },
          })
        } catch (err) {
          showError(t('routes.workflows.scopeDropFailed'), err)
        }
      },
      [t],
    )

    const ouNames = useMemo(() => {
      const names: Record<string, string> = {}
      ous.forEach((ou) => {
        names[ou.id] = ou.name
      })
      return names
    }, [ous])

    const statusFiltered = useMemo(
      () =>
        statusFilter === 'all'
          ? templates
          : templates.filter((t) => (t.status ?? 'active') === statusFilter),
      [templates, statusFilter],
    )

    const ouCounts = useMemo(() => {
      const counts: Record<string, number> = {}
      if (activeTab === 'install-images') {
        // Per-OU count = 1 when the unit sets a default install image; the
        // tree filters images to "what this unit installs".
        ous.forEach((ou) => {
          counts[ou.id] = ou.os_image_id ? 1 : 0
        })
        return counts
      }
      if (activeTab === 'backups' || activeTab === 'gold-images') {
        return captureCountsByOU
      }
      if (activeTab === 'runs') {
        // Approximate: counted over the fetched page only (the list is
        // server-paginated); selecting a unit shows the true filtered total.
        ous.forEach((ou) => {
          counts[ou.id] = executions.filter((e) =>
            runMatchesOU(e, ou.id),
          ).length
        })
        return counts
      }
      ous.forEach((ou) => {
        counts[ou.id] = statusFiltered.filter((t) =>
          scopeMatches(t, ou.id),
        ).length
      })
      return counts
    }, [
      activeTab,
      statusFiltered,
      executions,
      ous,
      scopeMatches,
      runMatchesOU,
      captureCountsByOU,
    ])

    const visibleTemplates = useMemo(() => {
      let list = statusFiltered
      if (selectedOU !== OU_FILTER_ALL) {
        list = list.filter((t) => scopeMatches(t, selectedOU))
      }
      const query = searchText.trim().toLowerCase()
      if (query) {
        list = list.filter(
          (t) =>
            t.name.toLowerCase().includes(query) ||
            (t.description || '').toLowerCase().includes(query),
        )
      }
      return list
    }, [statusFiltered, selectedOU, searchText, scopeMatches])

    const selectedExecutionId =
      search.sidebar === SidebarEnum.WorkflowExecutionDetails &&
      search.executionId
        ? search.executionId
        : undefined

    // The rail's meaning per tab: playbooks = availability, runs = target
    // subtree, install images = the unit's default install image, backups and
    // gold images = captures from the unit's nodes (by source node).
    const allRowLabel: Record<TabType, string> = {
      templates: t('routes.workflows.rail.allPlaybooks'),
      runs: t('routes.workflows.rail.allRuns'),
      'install-images': t('pages.osImages.rail.allImages'),
      backups: t('routes.workflows.rail.allBackups'),
      'gold-images': t('routes.workflows.rail.allGoldImages'),
    }
    const allRowCount =
      activeTab === 'runs'
        ? executionsTotal
        : activeTab === 'templates'
          ? statusFiltered.length
          : kindImagesCount

    return (
      <div className="h-full w-full flex">
        <OURailAside
          counts={ouCounts}
          // Playbook counts are availability sets (a global playbook counts in
          // every unit) and run counts already match the whole subtree, so the
          // rail's root roll-up would double-count — show raw counts there.
          // Image and capture counts are disjoint per unit; roll-up is right.
          rawRootCounts={activeTab === 'templates' || activeTab === 'runs'}
          selected={selectedOU}
          onSelect={handleOUSelect}
          pseudoRows={[
            {
              value: OU_FILTER_ALL,
              label: allRowLabel[activeTab],
              count: allRowCount,
              // Accepts a drop, which is how a playbook's scope is CLEARED.
              // Without it the rail could only ever widen a scope: dropping
              // adds a unit, and nothing anywhere took one away.
              dropTarget: activeTab === 'templates' ? null : undefined,
            },
          ]}
          dropTypes={
            activeTab === 'templates'
              ? [WORKFLOW_DND_TYPE]
              : activeTab === 'install-images'
                ? [IMAGE_DND_TYPE]
                : []
          }
          onDropItem={
            activeTab === 'templates'
              ? (itemId, ouId) => handleDropWorkflow(itemId, ouId)
              : activeTab === 'install-images'
                ? (itemId, ouId) => handleDropImage(itemId, ouId)
                : undefined
          }
          dropHint={
            activeTab === 'templates'
              ? t('routes.workflows.rail.dropHint')
              : activeTab === 'install-images'
                ? t('pages.osImages.rail.dropHint')
                : undefined
          }
          // Accept drops without OU EDIT MODE. Edit mode is gated on ou.manage,
          // which no role below super_admin holds, while the routes these drops
          // call need only playbook.author (re-target a playbook's scope) and
          // image.manage (set a unit's default install image). So the editor
          // told an author to "drag the playbook onto a unit" and the rail
          // silently refused every drop — with no other affordance anywhere.
          //
          // Gated on the right the DROP needs, per tab, rather than on edit
          // mode: the backend gates each route, and offering a drop that is
          // going to 403 is the shape of mistake the playbook action menu was
          // fixed for.
          alwaysAcceptDrops={
            activeTab === 'templates'
              ? canAuthor
              : activeTab === 'install-images'
                ? canManageImages
                : false
          }
        />

        <div className="flex-1 min-w-0 overflow-y-auto p-4 pt-6 md:px-8 md:pb-8">
          <h1 className="text-lg uppercase font-bold mb-6">
            {t('routes.workflows.title')}
          </h1>
          {/* Tab switcher: five tabs scroll horizontally rather than clipping
              on narrow screens; the Playbooks actions live on their own row
              below (like the Install images tab's upload button).
              overflow-y-hidden: the active tab's -mb-px underline overflows
              the strip by a pixel, and overflow-x-auto alone turns that into
              a vertical scrollbar at the strip's right end. */}
          <div className="flex items-center gap-2 mb-4 border-b border-neutral-11 overflow-x-auto overflow-y-hidden">
            {TAB_VALUES.map((tab) => (
              <button
                key={tab}
                type="button"
                onClick={() => handleTabChange(tab)}
                className={clsx(
                  'px-3 py-2 -mb-px font-mono text-sm transition-colors border-b-2 whitespace-nowrap',
                  activeTab === tab
                    ? 'text-lime-6 border-lime-6'
                    : 'text-neutral-4 hover:text-neutral-2 border-transparent',
                )}
              >
                {tabLabels()[tab]}
              </button>
            ))}
          </div>
          {activeTab === 'templates' && canAuthor && (
            <div className="flex flex-wrap justify-end gap-2 mb-4">
              <input
                ref={importInputRef}
                type="file"
                accept=".json,application/json"
                className="hidden"
                onChange={handleImportFile}
              />
              <Button
                size="md"
                kind="secondary"
                onClick={() => importInputRef.current?.click()}
              >
                {t('routes.workflows.actions.import')}
              </Button>
              <Button
                size="md"
                kind="secondary"
                onClick={handleOpenStarterPicker}
              >
                {t('routes.workflows.actions.fromExample')}
              </Button>
              <Button size="md" onClick={handleCreateClick}>
                {t('routes.workflows.actions.create')}
              </Button>
            </div>
          )}

          {/* Content */}
          {imageKind ? (
            <OperationsImagesTab kind={imageKind} />
          ) : activeTab === 'templates' ? (
            <>
              <div className="mb-4 flex items-center gap-3">
                <input
                  type="text"
                  value={searchText}
                  onChange={(e) => setSearchText(e.target.value)}
                  className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5 w-full max-w-xs"
                  placeholder={t('routes.workflows.searchPlaceholder')}
                />
                <div className="flex rounded border border-neutral-11 overflow-hidden">
                  {(['all', 'active', 'draft'] as StatusFilter[]).map(
                    (status) => (
                      <button
                        key={status}
                        type="button"
                        onClick={() => setStatusFilter(status)}
                        className={clsx(
                          'px-3 py-2 text-xs uppercase transition-colors',
                          statusFilter === status
                            ? 'bg-neutral-12 text-lime-6'
                            : 'text-neutral-5 hover:text-neutral-2',
                        )}
                      >
                        {t(`routes.workflows.statusFilter.${status}`)}
                      </button>
                    ),
                  )}
                </div>
              </div>
              {/* Row actions follow the same gate. Passed as undefined rather
                  than as no-ops so the table renders nothing at all: a disabled
                  button that never becomes enabled is a worse answer than an
                  absent one. */}
              <WorkflowTable
                templates={visibleTemplates}
                ouNames={ouNames}
                onRun={canRun ? setRunTarget : undefined}
                onEdit={canAuthor ? handleEdit : undefined}
                onClone={canAuthor ? handleClone : undefined}
                onExport={canAuthor ? handleExport : undefined}
                onDelete={canAuthor ? setDeleteTarget : undefined}
              />
              <ConfirmDialog
                open={!!deleteTarget}
                title={t('routes.workflows.deleteDialog.title')}
                message={t('routes.workflows.deleteDialog.message', {
                  name: deleteTarget?.name,
                })}
                confirmText={t('routes.workflows.deleteDialog.confirm')}
                destructive
                busy={isDeleting}
                onConfirm={handleConfirmDelete}
                onCancel={() => setDeleteTarget(null)}
              />
              <RunPlaybookDialog
                template={runTarget}
                onClose={() => setRunTarget(null)}
                onStarted={(executionId) => {
                  setRunTarget(null)
                  handleExecutionClick(executionId)
                }}
              />
              <StarterPickerDialog
                open={starterPickerOpen}
                starters={starters}
                loading={startersLoading}
                busy={starterBusy}
                error={starterError}
                onSelect={handleSelectStarter}
                onClose={() => setStarterPickerOpen(false)}
              />
              {/* Import outcome: success opens the editor, warnings listed inline */}
              <ConfirmDialog
                open={importOutcome?.kind === 'success'}
                title={t('routes.workflows.importedDialog.title')}
                message={
                  importOutcome?.kind === 'success' ? (
                    <div className="flex flex-col gap-2">
                      <span>
                        {t('routes.workflows.importedDialog.createdIntro', {
                          name: importOutcome.template.name,
                        })}{' '}
                        <span className="text-neutral-2">
                          {t('routes.workflows.importedDialog.draftWord')}
                        </span>
                        {t('routes.workflows.importedDialog.createdSuffix')}
                      </span>
                      {importOutcome.warnings.length > 0 && (
                        <ul className="list-disc pl-4 text-yellow-5 flex flex-col gap-1">
                          {importOutcome.warnings.map((warning) => (
                            <li key={warning}>{warning}</li>
                          ))}
                        </ul>
                      )}
                    </div>
                  ) : null
                }
                confirmText={t('routes.workflows.importedDialog.openInEditor')}
                cancelText={t('routes.workflows.importedDialog.close')}
                onConfirm={() => {
                  if (importOutcome?.kind !== 'success') return
                  const templateId = importOutcome.template.id
                  setImportOutcome(null)
                  navigate({
                    to: '/workflows/$templateId/edit',
                    params: { templateId },
                    search: { sidebar: undefined },
                  })
                }}
                onCancel={() => setImportOutcome(null)}
              />
              <ConfirmDialog
                open={importOutcome?.kind === 'error'}
                title={t('routes.workflows.importFailedDialog.title')}
                message={
                  importOutcome?.kind === 'error' ? importOutcome.message : ''
                }
                confirmText={t('routes.workflows.importFailedDialog.close')}
                hideCancel
                onConfirm={() => setImportOutcome(null)}
                onCancel={() => setImportOutcome(null)}
              />
            </>
          ) : (
            <>
              {/* Filter by playbook */}
              <div className="mb-4">
                <label className="text-sm font-medium text-neutral-7 uppercase mb-2 block">
                  {t('routes.workflows.runsFilterLabel')}
                </label>
                <select
                  value={search.templateId || ''}
                  onChange={(e) =>
                    handleTemplateFilterChange(e.target.value || undefined)
                  }
                  className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5 w-full max-w-xs"
                >
                  <option value="">
                    {t('routes.workflows.runsFilterAll')}
                  </option>
                  {templates.map((template) => (
                    <option key={template.id} value={template.id}>
                      {template.name}
                    </option>
                  ))}
                </select>
              </div>
              <WorkflowExecutionList
                selectedExecutionId={selectedExecutionId}
                onExecutionClick={handleExecutionClick}
              />
              {/* Server-side pager (same layout as the secrets list) */}
              <div className="flex items-center justify-between gap-4 mt-4 font-mono text-xs text-neutral-5">
                <div className="flex items-center gap-2">
                  <span>{t('routes.workflows.pager.perPage')}</span>
                  <select
                    value={runsPageSize}
                    onChange={(e) => {
                      const next = Number(e.target.value)
                      setRunsPageSize(next)
                      localStorage.setItem(RUNS_PAGE_SIZE_KEY, String(next))
                      setRunsPage(1)
                    }}
                    className="bg-neutral-13 rounded px-2 py-1 text-xs text-white outline-none"
                  >
                    {RUNS_PAGE_SIZES.map((size) => (
                      <option key={size} value={size}>
                        {size}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="flex items-center gap-3">
                  <span>
                    {executionsLoading
                      ? t('routes.workflows.pager.loading')
                      : t('routes.workflows.pager.range', {
                          from:
                            executionsTotal === 0
                              ? 0
                              : (runsPage - 1) * runsPageSize + 1,
                          to: Math.min(
                            runsPage * runsPageSize,
                            executionsTotal,
                          ),
                          total: executionsTotal,
                        })}
                  </span>
                  <button
                    type="button"
                    onClick={() => setRunsPage((p) => Math.max(1, p - 1))}
                    disabled={runsPage <= 1}
                    className="px-2 py-1 text-neutral-4 hover:text-neutral-1 disabled:opacity-40 disabled:cursor-not-allowed"
                    aria-label={t('routes.workflows.pager.previousPage')}
                  >
                    ‹
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      setRunsPage((p) =>
                        Math.min(
                          Math.max(
                            1,
                            Math.ceil(executionsTotal / runsPageSize),
                          ),
                          p + 1,
                        ),
                      )
                    }
                    disabled={
                      runsPage >=
                      Math.max(1, Math.ceil(executionsTotal / runsPageSize))
                    }
                    className="px-2 py-1 text-neutral-4 hover:text-neutral-1 disabled:opacity-40 disabled:cursor-not-allowed"
                    aria-label={t('routes.workflows.pager.nextPage')}
                  >
                    ›
                  </button>
                </div>
              </div>
            </>
          )}
        </div>
      </div>
    )
  },
  loader: async () => {
    // allSettled: playbooks and the image inventory are separate grants; a
    // role holding one and not the other must still get a usable page.
    await Promise.allSettled([
      fetchWorkflowTemplatesFx(),
      imagesModel.fetchImagesFx(),
    ])
  },
})
