import clsx from 'clsx'
import { FC, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import type { NodeFile, TrackedFile } from '@/entities/node/api/types'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { InfoRow, SectionDivider } from './infoRow.tsx'
import { SubViewHeader } from './subViewHeader.tsx'

const POLL_INTERVAL_MS = 1500
const POLL_DEADLINE_MS = 60_000

type Step = 'edit' | 'confirmDelete'

const statusTone = (status?: string): string => {
  switch (status) {
    case 'applied':
      return 'text-green-5'
    case 'applying':
      return 'text-yellow-5'
    case 'failed':
      return 'text-red-5'
    default:
      return 'text-neutral-4'
  }
}

// NodeFileEditor is one tracked file: its metadata, its content, its version
// history, and the destructive verbs. Content is fetched separately from the
// list, which carries metadata only.
export const NodeFileEditor: FC<{
  nodeId: string
  file: TrackedFile
  canWrite: boolean
  online: boolean
  inMaintenance: boolean
  onBack: () => void
  onGone: () => void
}> = ({
  nodeId,
  file: initialFile,
  canWrite,
  online,
  inMaintenance,
  onBack,
  onGone,
}) => {
  const { t } = useTranslation()
  // The tracked record is seeded from the list and updated from every read, so
  // a successful refresh clears the "never read" and "permissions unknown"
  // banners instead of leaving them contradicting what just happened.
  const [file, setFile] = useState<TrackedFile>(initialFile)
  useEffect(() => setFile(initialFile), [initialFile])
  const [step, setStep] = useState<Step>('edit')
  const [versions, setVersions] = useState<NodeFile[]>([])
  const [selected, setSelected] = useState<NodeFile | null>(null)
  const [content, setContent] = useState('')
  const [draft, setDraft] = useState<string | null>(null) // non-null while editing
  const [busy, setBusy] = useState<
    null | 'loading' | 'saving' | 'applying' | 'refreshing' | 'untracking'
  >('loading')
  // Whether the current buffer really came from the platform. Editing is
  // refused until it did, so a failed read can never be saved as content.
  const [contentLoaded, setContentLoaded] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [confirmPath, setConfirmPath] = useState('')
  const [confirmUntrack, setConfirmUntrack] = useState(false)
  const alive = useRef(true)

  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [nodeId, file.path])

  // Load the version list and the latest content. Content and the draft are
  // written here and nowhere else, so an async refresh cannot overwrite what the
  // operator is typing.
  const load = useCallback(async () => {
    try {
      setBusy('loading')
      setError(null)
      const list = await apiNodes.getFileVersions(nodeId, file.path)
      // Content is fetched separately and its failure is NOT swallowed: an
      // empty editor for a file that is not empty invites a Save that stores
      // the emptiness and an Apply that truncates the real file.
      const latest = file.never_read
        ? null
        : await apiNodes.getFile(nodeId, file.path)
      if (!alive.current) return
      setVersions(list.versions)
      setSelected(latest ?? list.versions[0] ?? null)
      setContent(latest?.content ?? '')
      setContentLoaded(!file.never_read)
    } catch (err) {
      if (alive.current)
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeFileEditor.loadError'),
          ),
        )
    } finally {
      if (alive.current) setBusy(null)
    }
  }, [nodeId, file.path, file.never_read, t])

  useEffect(() => {
    load()
  }, [load])

  const selectVersion = useCallback(
    async (version: NodeFile) => {
      if (draft !== null) return
      setSelected(version)
      // The versions list carries metadata only, so the content of the version
      // the user picked has to be fetched BY VERSION. Fetching the latest here
      // showed one version's bytes while Apply sent another's.
      try {
        const full = await apiNodes.getFile(nodeId, file.path, version.version)
        if (!alive.current) return
        setContent(full.content ?? '')
        setContentLoaded(true)
      } catch (err) {
        if (alive.current) {
          setError(
            extractApiErrorMessage(
              err,
              t('widgets.sidebars.node.nodeFileEditor.versionLoadError'),
            ),
          )
          setContentLoaded(false)
        }
      }
    },
    [draft, nodeId, file.path, t],
  )

  const save = useCallback(async () => {
    if (draft === null) return
    try {
      setBusy('saving')
      setError(null)
      await apiNodes.updateFile(nodeId, file.path, draft)
      const list = await apiNodes.getFileVersions(nodeId, file.path)
      if (!alive.current) return
      setVersions(list.versions)
      setSelected(list.versions[0] ?? null)
      setContent(draft)
      setDraft(null)
      setNotice(t('widgets.sidebars.node.nodeFileEditor.savedNotice'))
    } catch (err) {
      if (alive.current)
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeFileEditor.saveError'),
          ),
        )
    } finally {
      if (alive.current) setBusy(null)
    }
  }, [draft, nodeId, file.path, t])

  const apply = useCallback(async () => {
    if (!selected?.id) return
    const version = selected.version
    try {
      setBusy('applying')
      setError(null)
      await apiNodes.applyFile(nodeId, selected.id)
      await nodesModel.fetchNodesFx()
      if (!alive.current) return
      setNotice(t('widgets.sidebars.node.nodeFileEditor.writingNotice'))

      // Then wait for the device to say what happened. Without this the notice
      // was the end of the story: the row stayed on the "applying" status the
      // dispatch wrote, and a device-side failure — a refused path, a full
      // disk — left an optimistic message on screen until the user navigated
      // away and back. The backend settles the version from the command's
      // terminal status; this polls the versions list for that settlement, the
      // same shape refresh already uses.
      const deadline = Date.now() + POLL_DEADLINE_MS
      for (;;) {
        await new Promise((r) => setTimeout(r, POLL_INTERVAL_MS))
        if (!alive.current) return
        const list = await apiNodes.getFileVersions(nodeId, file.path)
        if (!alive.current) return
        setVersions(list.versions)
        const settled = list.versions.find((v) => v.version === version)
        if (settled?.status === 'applied') {
          setNotice(t('widgets.sidebars.node.nodeFileEditor.appliedNotice'))
          return
        }
        if (settled?.status === 'failed') {
          setNotice(null)
          setError(
            settled.error_message ||
              t('widgets.sidebars.node.nodeFileEditor.applyRefused'),
          )
          return
        }
        if (Date.now() > deadline) {
          setNotice(t('widgets.sidebars.node.nodeFileEditor.applyUnconfirmed'))
          return
        }
      }
    } catch (err) {
      if (alive.current)
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeFileEditor.applyError'),
          ),
        )
    } finally {
      if (alive.current) setBusy(null)
    }
  }, [selected, nodeId, file.path, t])

  const refresh = useCallback(async () => {
    try {
      setBusy('refreshing')
      setError(null)
      setNotice(null)
      const commandId = await apiNodes.refreshFile(nodeId, file.path)
      const deadline = Date.now() + POLL_DEADLINE_MS
      for (;;) {
        if (!alive.current) return
        await new Promise((r) => setTimeout(r, POLL_INTERVAL_MS))
        if (!alive.current) return
        const result = await apiNodes.getFileReadResult(
          nodeId,
          commandId,
          file.path,
        )
        if (result.status === 'completed') {
          setNotice(
            result.changed
              ? t('widgets.sidebars.node.nodeFileEditor.versionAdded', {
                  version: result.version,
                })
              : t('widgets.sidebars.node.nodeFileEditor.upToDate'),
          )
          setFile((current) => ({
            ...current,
            never_read: false,
            size: result.size ?? current.size,
            mode: result.mode || current.mode,
            owner: result.owner || current.owner,
            group: result.group || current.group,
            can_apply: !!(result.mode && result.owner) || current.can_apply,
          }))
          await load()
          return
        }
        if (result.status === 'failed') {
          setError(
            result.detail ||
              t('widgets.sidebars.node.nodeFileEditor.readFailed'),
          )
          return
        }
        if (Date.now() > deadline) {
          setError(t('widgets.sidebars.node.nodeFileEditor.readTimeout'))
          return
        }
      }
    } catch (err) {
      if (alive.current)
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeFileEditor.refreshError'),
          ),
        )
    } finally {
      if (alive.current) setBusy(null)
    }
  }, [nodeId, file.path, load, t])

  const untrack = useCallback(async () => {
    if (!confirmUntrack) {
      setConfirmUntrack(true)
      return
    }
    try {
      setBusy('untracking')
      setError(null)
      await apiNodes.untrackFile(nodeId, file.path)
      if (alive.current) onGone()
    } catch (err) {
      if (alive.current)
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeFileEditor.untrackError'),
          ),
        )
      if (alive.current) setBusy(null)
    }
  }, [nodeId, file.path, onGone, t])

  const deleteOnDevice = useCallback(async () => {
    try {
      setBusy('untracking')
      setError(null)
      await apiNodes.deleteFileOnDevice(nodeId, file.path, confirmPath, true)
      if (alive.current) onGone()
    } catch (err) {
      if (alive.current) {
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeFileEditor.deleteError'),
          ),
        )
        setBusy(null)
      }
    }
  }, [nodeId, file.path, confirmPath, onGone, t])

  if (step === 'confirmDelete') {
    return (
      <div className="flex flex-col gap-4">
        <SubViewHeader
          title={t('widgets.sidebars.node.nodeFileEditor.deleteTitle')}
          onBack={() => {
            setStep('edit')
            setConfirmPath('')
            setError(null)
          }}
        />
        <div className="text-sm text-neutral-4">
          {t('widgets.sidebars.node.nodeFileEditor.deleteWarningBefore')}{' '}
          <span className="font-mono">{file.path}.orchestrator.deleted</span>
          {t('widgets.sidebars.node.nodeFileEditor.deleteWarningAfter')}
        </div>
        <div className="rounded border border-neutral-12 bg-neutral-13 px-3 py-2 font-mono text-sm text-neutral-3">
          {file.path}
        </div>
        <label htmlFor="delete-confirm-path" className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.nodeFileEditor.deleteConfirmLabel')}
        </label>
        <input
          id="delete-confirm-path"
          type="text"
          value={confirmPath}
          onChange={(e) => {
            setConfirmPath(e.target.value)
            setError(null)
          }}
          placeholder={file.path}
          autoFocus
          className="rounded border border-neutral-12 bg-neutral-15 px-3 py-2 font-mono text-sm text-neutral-2 focus:border-red-6 focus:outline-none"
        />
        {error && (
          <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-sm text-red-6">
            {error}
          </div>
        )}
        <div className="flex gap-2">
          <Button
            variant="destructive"
            onClick={deleteOnDevice}
            disabled={busy !== null || confirmPath !== file.path}
            grow
          >
            {busy
              ? t('widgets.sidebars.node.nodeFileEditor.deleting')
              : t('widgets.sidebars.node.nodeFileEditor.deleteOnDevice')}
          </Button>
          <Button
            variant="normal"
            kind="secondary"
            onClick={() => {
              setStep('edit')
              setConfirmPath('')
            }}
            disabled={busy !== null}
          >
            {t('widgets.sidebars.node.nodeFileEditor.cancel')}
          </Button>
        </div>
      </div>
    )
  }

  const editing = draft !== null
  const disabledReason = !online
    ? t('widgets.sidebars.node.nodeFileEditor.nodeMustBeOnline')
    : inMaintenance
      ? t('widgets.sidebars.node.nodeFileEditor.netbootTitle')
      : undefined

  return (
    <div className="flex flex-col gap-4">
      <SubViewHeader title={file.path} onBack={onBack} />

      <div className="flex flex-col gap-2">
        <InfoRow label={t('widgets.sidebars.node.nodeFileEditor.sourceLabel')}>
          {file.protected
            ? t('widgets.sidebars.node.nodeFileEditor.sourceManaged')
            : t('widgets.sidebars.node.nodeFileEditor.sourceUser')}
        </InfoRow>
        {file.mode && (
          <InfoRow label={t('widgets.sidebars.node.nodeFileEditor.modeLabel')}>
            {file.mode}
          </InfoRow>
        )}
        {file.owner && (
          <InfoRow label={t('widgets.sidebars.node.nodeFileEditor.ownerLabel')}>
            {file.owner}
            {file.group ? `:${file.group}` : ''}
          </InfoRow>
        )}
        {selected && (
          <InfoRow
            label={t('widgets.sidebars.node.nodeFileEditor.versionLabel')}
          >
            <span className={statusTone(selected.status)}>
              v{selected.version} · {selected.status}
            </span>
          </InfoRow>
        )}
      </div>

      {file.never_read && (
        <div className="rounded border border-neutral-11 bg-neutral-13 px-3 py-2 text-xs text-neutral-5">
          {t('widgets.sidebars.node.nodeFileEditor.neverReadBanner')}
        </div>
      )}

      {!file.can_apply && !file.never_read && (
        <div className="rounded border border-amber-6/40 bg-amber-6/10 px-3 py-2 text-xs text-amber-6">
          {t('widgets.sidebars.node.nodeFileEditor.permissionsBanner')}
        </div>
      )}

      {notice && <div className="text-xs text-lime-6">{notice}</div>}
      {error && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-sm text-red-6">
          {error}
        </div>
      )}

      <div className="flex flex-col gap-2">
        <SectionDivider
          label={t('widgets.sidebars.node.nodeFileEditor.contentTitle')}
        />
        {busy === 'loading' ? (
          <div className="text-sm text-neutral-6">
            {t('widgets.sidebars.node.nodeFileEditor.loading')}
          </div>
        ) : (
          <textarea
            aria-label={t(
              'widgets.sidebars.node.nodeFileEditor.contentsAriaLabel',
              { path: file.path },
            )}
            value={editing ? (draft as string) : content}
            onChange={(e) => setDraft(e.target.value)}
            disabled={!canWrite || busy !== null || !contentLoaded}
            spellCheck={false}
            className={clsx(
              'min-h-[16rem] font-mono text-xs bg-neutral-15 border border-neutral-12 rounded p-3 text-neutral-2 resize-y',
              !editing && 'opacity-90',
            )}
          />
        )}
        {canWrite && (
          <div className="flex flex-wrap gap-2">
            {editing && (
              <>
                <Button
                  variant="normal"
                  onClick={save}
                  disabled={busy !== null}
                >
                  {busy === 'saving'
                    ? t('widgets.sidebars.node.nodeFileEditor.saving')
                    : t('widgets.sidebars.node.nodeFileEditor.saveVersion')}
                </Button>
                <Button
                  variant="normal"
                  kind="secondary"
                  onClick={() => setDraft(null)}
                  disabled={busy !== null}
                >
                  {t('widgets.sidebars.node.nodeFileEditor.discardChanges')}
                </Button>
              </>
            )}
            {!editing && (
              <Button
                variant="normal"
                onClick={apply}
                disabled={
                  busy !== null ||
                  !selected?.id ||
                  !file.can_apply ||
                  !online ||
                  inMaintenance
                }
                title={disabledReason}
              >
                {busy === 'applying'
                  ? t('widgets.sidebars.node.nodeFileEditor.applying')
                  : t('widgets.sidebars.node.nodeFileEditor.applyToDevice')}
              </Button>
            )}
            <Button
              variant="normal"
              kind="secondary"
              onClick={refresh}
              disabled={busy !== null || !online || inMaintenance}
              title={disabledReason}
            >
              {busy === 'refreshing'
                ? t('widgets.sidebars.node.nodeFileEditor.reading')
                : t('widgets.sidebars.node.nodeFileEditor.refreshFromDevice')}
            </Button>
          </div>
        )}
      </div>

      {versions.length > 0 && (
        <div className="flex flex-col gap-2">
          <SectionDivider
            label={t('widgets.sidebars.node.nodeFileEditor.versionsTitle')}
          />
          <div className="flex flex-col">
            {versions.map((version) => (
              <button
                key={version.version}
                type="button"
                onClick={() => selectVersion(version)}
                disabled={editing}
                className={clsx(
                  'flex items-center justify-between gap-2 rounded px-2 py-1 text-xs transition-colors',
                  selected?.version === version.version
                    ? 'bg-lime-6/10 text-neutral-2'
                    : 'text-neutral-5 hover:bg-neutral-13',
                )}
              >
                <span>v{version.version}</span>
                <span className={statusTone(version.status)}>
                  {version.status}
                </span>
                <span className="text-neutral-7">
                  {new Date(version.created_at).toLocaleString()}
                </span>
              </button>
            ))}
          </div>
        </div>
      )}

      {canWrite && (
        <div className="flex flex-col gap-2">
          <SectionDivider
            label={t('widgets.sidebars.node.nodeFileEditor.danger')}
            tone="danger"
          />
          <Button
            variant="destructive"
            kind="secondary"
            onClick={untrack}
            disabled={busy !== null || file.protected}
            title={
              file.protected
                ? t('widgets.sidebars.node.nodeFileEditor.managedNote')
                : t('widgets.sidebars.node.nodeFileEditor.untrackTitle')
            }
            grow
          >
            {busy === 'untracking'
              ? t('widgets.sidebars.node.nodeFileEditor.removing')
              : confirmUntrack
                ? t('widgets.sidebars.node.nodeFileEditor.untrackConfirm')
                : t('widgets.sidebars.node.nodeFileEditor.untrack')}
          </Button>
          <Button
            variant="destructive"
            onClick={() => setStep('confirmDelete')}
            disabled={
              busy !== null || file.protected || !online || inMaintenance
            }
            title={
              file.protected
                ? t('widgets.sidebars.node.nodeFileEditor.managedNote')
                : disabledReason
            }
            grow
          >
            {t('widgets.sidebars.node.nodeFileEditor.deleteOnDevice')}
          </Button>
          {file.protected && (
            <div className="text-xs text-neutral-6">
              {t('widgets.sidebars.node.nodeFileEditor.managedNote')}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
