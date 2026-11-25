import clsx from 'clsx'
import { FC, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiNodes } from '@/entities/node/api'
import type { TrackedFile } from '@/entities/node/api/types'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SectionDivider } from './infoRow.tsx'
import { SubViewHeader } from './subViewHeader.tsx'

// Suggestions for the Add form. Deliberately files an operator edits by hand
// that exist on a stock Debian or Ubuntu Raspberry Pi install; config.txt is
// already tracked by the platform. A service whose package may be absent
// (timesyncd on an image that ships chrony) or a file the distribution does
// not create (rc.local) fails with "no such file", so neither is offered.
// sshd_config is not offered either: a device agent that does not check it
// with sshd before the write lets a typo stop sshd at its next start.
const SUGGESTED_PATHS = [
  '/etc/hosts',
  '/etc/hostname',
  '/boot/firmware/cmdline.txt',
]

const POLL_INTERVAL_MS = 1500
const POLL_DEADLINE_MS = 60_000

type Step = 'list' | 'add'

const Badge: FC<{ tone: 'managed' | 'drift' | 'unread'; title: string }> = ({
  tone,
  title,
}) => {
  const { t } = useTranslation()
  return (
    <span
      title={title}
      className={clsx(
        'flex-shrink-0 text-xxs uppercase tracking-wide px-1.5 py-0.5 rounded border',
        tone === 'managed' && 'border-lime-6/40 bg-lime-6/10 text-lime-6',
        tone === 'drift' && 'border-amber-6/40 bg-amber-6/10 text-amber-6',
        tone === 'unread' && 'border-neutral-9 bg-neutral-13 text-neutral-6',
      )}
    >
      {tone === 'managed'
        ? t('widgets.sidebars.node.nodeFilesSection.badgeManaged')
        : tone === 'drift'
          ? t('widgets.sidebars.node.nodeFilesSection.badgeDrift')
          : t('widgets.sidebars.node.nodeFilesSection.badgeUnread')}
    </span>
  )
}

// An empty file and a file we have never read are different facts, and the
// list has to show them differently.
const formatSize = (file: TrackedFile): string => {
  if (file.never_read) return '—'
  if (file.size < 1024) return `${file.size} B`
  return `${(file.size / 1024).toFixed(1)} KB`
}

// NodeFilesSection is the Files list under Advanced: every file the platform
// follows on this node, plus adding a new one. Editing, refreshing and removing
// happen in the editor one step deeper (onOpen).
export const NodeFilesSection: FC<{
  nodeId: string
  canWrite: boolean
  online: boolean
  inMaintenance: boolean
  onOpen: (file: TrackedFile) => void
  onBack: () => void
}> = ({ nodeId, canWrite, online, inMaintenance, onOpen, onBack }) => {
  const { t } = useTranslation()
  const [files, setFiles] = useState<TrackedFile[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [step, setStep] = useState<Step>('list')

  const load = useCallback(async () => {
    try {
      setLoading(true)
      setError(null)
      setFiles(await apiNodes.listTrackedFiles(nodeId))
    } catch (err) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.nodeFilesSection.loadError'),
        ),
      )
    } finally {
      setLoading(false)
    }
  }, [nodeId, t])

  useEffect(() => {
    load()
  }, [load])

  if (step === 'add') {
    return (
      <NodeFileTrackForm
        nodeId={nodeId}
        existing={files.map((f) => f.path)}
        online={online}
        inMaintenance={inMaintenance}
        onBack={() => setStep('list')}
        onTracked={async () => {
          setStep('list')
          await load()
        }}
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <SubViewHeader
        title={t('widgets.sidebars.node.nodeFilesSection.title')}
        onBack={onBack}
      />

      <p className="text-xs text-neutral-6">
        {t('widgets.sidebars.node.nodeFilesSection.description')}
      </p>

      {loading && (
        <div className="text-sm text-neutral-6">
          {t('widgets.sidebars.node.nodeFilesSection.loading')}
        </div>
      )}

      {error && !loading && (
        <div className="text-sm text-status-error">
          {error}
          <button
            type="button"
            onClick={load}
            className="ml-2 text-xs underline"
          >
            {t('widgets.sidebars.node.nodeFilesSection.retry')}
          </button>
        </div>
      )}

      {!loading && !error && files.length === 0 && (
        <div className="text-sm text-neutral-6">
          {t('widgets.sidebars.node.nodeFilesSection.empty')}
        </div>
      )}

      {!loading && !error && files.length > 0 && (
        <div className="flex flex-col">
          {files.map((file) => (
            <button
              key={file.path}
              type="button"
              onClick={() => onOpen(file)}
              className="flex items-center gap-2 text-left hover:bg-neutral-13 rounded px-2 py-1.5 transition-colors"
            >
              <span className="flex-1 min-w-0 text-xs font-mono text-neutral-4 truncate">
                {file.path}
              </span>
              {file.protected && (
                <Badge
                  tone="managed"
                  title={t(
                    'widgets.sidebars.node.nodeFilesSection.managedTitle',
                  )}
                />
              )}
              {file.never_read && (
                <Badge
                  tone="unread"
                  title={t(
                    'widgets.sidebars.node.nodeFilesSection.unreadTitle',
                  )}
                />
              )}
              <span className="flex-shrink-0 text-xxs text-neutral-7 w-16 text-right">
                {formatSize(file)}
              </span>
            </button>
          ))}
        </div>
      )}

      {canWrite && (
        <>
          <SectionDivider
            label={t('widgets.sidebars.node.nodeFilesSection.addDivider')}
          />
          <Button
            variant="normal"
            kind="secondary"
            onClick={() => setStep('add')}
            disabled={!online || inMaintenance}
            title={
              inMaintenance
                ? t('widgets.sidebars.node.nodeFilesSection.netbootTitle')
                : !online
                  ? t('widgets.sidebars.node.nodeFilesSection.nodeMustBeOnline')
                  : t('widgets.sidebars.node.nodeFilesSection.addTitle')
            }
            grow
          >
            {t('widgets.sidebars.node.nodeFilesSection.addFile')}
          </Button>
        </>
      )}
      {!canWrite && (
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.nodeFilesSection.noPermission')}
        </div>
      )}
    </div>
  )
}

// NodeFileTrackForm reads a path off the device and starts tracking it. The
// read is an agent command, so this dispatches and polls — with a liveness ref
// so a closed panel stops polling and can never render another node's result.
const NodeFileTrackForm: FC<{
  nodeId: string
  existing: string[]
  online: boolean
  inMaintenance: boolean
  onBack: () => void
  onTracked: () => void
}> = ({ nodeId, existing, online, inMaintenance, onBack, onTracked }) => {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [state, setState] = useState<'idle' | 'reading' | 'error'>('idle')
  const [error, setError] = useState<string | null>(null)
  const alive = useRef(true)

  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [nodeId])

  const submit = useCallback(async () => {
    const trimmed = path.trim()
    if (!trimmed.startsWith('/')) {
      setError(t('widgets.sidebars.node.nodeFilesSection.absolutePathError'))
      setState('error')
      return
    }
    if (existing.includes(trimmed)) {
      setError(t('widgets.sidebars.node.nodeFilesSection.alreadyTracked'))
      setState('error')
      return
    }

    setState('reading')
    setError(null)
    try {
      const commandId = await apiNodes.trackFile(nodeId, trimmed)
      const deadline = Date.now() + POLL_DEADLINE_MS
      for (;;) {
        if (!alive.current) return
        await new Promise((r) => setTimeout(r, POLL_INTERVAL_MS))
        if (!alive.current) return
        const result = await apiNodes.getFileReadResult(
          nodeId,
          commandId,
          trimmed,
        )
        if (result.status === 'completed') {
          onTracked()
          return
        }
        if (result.status === 'failed') {
          setError(
            result.detail ||
              t('widgets.sidebars.node.nodeFilesSection.readFailed'),
          )
          setState('error')
          return
        }
        if (Date.now() > deadline) {
          setError(t('widgets.sidebars.node.nodeFilesSection.readTimeout'))
          setState('error')
          return
        }
      }
    } catch (err) {
      if (!alive.current) return
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.nodeFilesSection.readError'),
        ),
      )
      setState('error')
    }
  }, [nodeId, path, existing, onTracked, t])

  const busy = state === 'reading'

  return (
    <div className="flex flex-col gap-4">
      <SubViewHeader
        title={t('widgets.sidebars.node.nodeFilesSection.addFile')}
        onBack={onBack}
      />

      <div className="flex flex-col gap-2">
        <label htmlFor="track-path" className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.nodeFilesSection.pathLabel')}
        </label>
        <input
          id="track-path"
          type="text"
          value={path}
          onChange={(e) => {
            setPath(e.target.value)
            setError(null)
            setState('idle')
          }}
          placeholder="/etc/hosts"
          disabled={busy}
          autoFocus
          className="rounded border border-neutral-12 bg-neutral-15 px-3 py-2 font-mono text-sm text-neutral-2 focus:border-lime-6 focus:outline-none disabled:opacity-50"
        />
      </div>

      <div className="flex flex-wrap gap-1.5">
        {SUGGESTED_PATHS.filter((p) => !existing.includes(p)).map((p) => (
          <button
            key={p}
            type="button"
            disabled={busy}
            onClick={() => setPath(p)}
            className="text-xxs font-mono px-2 py-1 rounded border border-neutral-11 text-neutral-5 hover:text-neutral-2 hover:border-neutral-9 transition-colors"
          >
            {p}
          </button>
        ))}
      </div>

      <div className="text-xs text-neutral-6">
        {t('widgets.sidebars.node.nodeFilesSection.storageNote')}
      </div>

      {error && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-sm text-red-6">
          {error}
        </div>
      )}

      <Button
        variant="normal"
        onClick={submit}
        disabled={busy || !path.trim() || !online || inMaintenance}
        grow
      >
        {busy
          ? t('widgets.sidebars.node.nodeFilesSection.reading')
          : t('widgets.sidebars.node.nodeFilesSection.readAndTrack')}
      </Button>
    </div>
  )
}
