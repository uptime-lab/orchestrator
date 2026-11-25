import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { agentUpdateState } from '@/entities/agent/version'
import { apiNodes } from '@/entities/node/api'
import { fetchNodeCommandsFx } from '@/entities/node/model'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError, showInfo } from '@/shared/ui/toast'

import { agentUpdateRefusal } from './agentUpdateRefusal.ts'
import { InfoRow } from './infoRow.tsx'
import { latestAgentVersion } from './latestAgentVersion.ts'

// Compact agent-version row for the node Overview: shows the reported agent
// version and either an "up to date" marker or a small Update button that
// self-updates the running agent to the latest CDN version.
//
// A targeted rollback to a specific/older version has no UI by decision:
// the picker that once sat under this row was deleted rather than kept as
// dead code. A rollback is `update_agent` with an explicit version through the API.
export const AgentVersionBadge: FC<{
  nodeId: string
  version?: string
  // "watcher" marks the telemetry-only agent: the row shows a pill instead of
  // update affordances (update_agent is a command, and the platform refuses
  // commands to a watcher).
  kind?: 'full' | 'watcher'
  online: boolean
  // Whether the viewer may trigger a self-update. The Overview row is the only
  // place the agent can be updated, so a viewer without the permission just
  // sees the version (or the up-to-date marker), no Update button.
  canUpdate: boolean
}> = ({ nodeId, version, kind, online, canUpdate }) => {
  const { t } = useTranslation()
  const [latest, setLatest] = useState<string | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    fetch('/cdn/agent-versions')
      .then((r) => (r.ok ? r.json() : { versions: [] }))
      .then((data) => {
        if (cancelled) return
        // What the Update button's `latest` installs (see latestAgentVersion).
        setLatest(latestAgentVersion(data))
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  const current =
    version && version.trim()
      ? version
      : t('widgets.sidebars.node.agentVersionBadge.unknown')
  // Ordered, not equal: a node newer than `latest` (a rollback, or an import
  // that left `latest` behind) is offered nothing, since the update would be
  // a downgrade.
  const updateState = agentUpdateState(version, latest)
  const upToDate = updateState === 'current'
  const offerUpdate = updateState === 'update'

  const handleUpdate = async () => {
    setBusy(true)
    try {
      await apiNodes.updateAgent(nodeId, 'latest')
      await fetchNodeCommandsFx(nodeId)
      if (!online) {
        showInfo(t('widgets.sidebars.node.agentVersionBadge.queued'))
      }
    } catch (err: unknown) {
      const refusal = agentUpdateRefusal(err)
      if (refusal === 'already-current') {
        showInfo(t('shared.commandRefusal.agentAlreadyCurrent'))
      } else if (refusal === 'refused') {
        showError(t('widgets.sidebars.node.agentVersionBadge.updateError'), err)
      }
    } finally {
      setBusy(false)
      setConfirmOpen(false)
    }
  }

  return (
    <InfoRow
      label={t('widgets.sidebars.node.agentVersionBadge.label')}
      align="center"
    >
      <div className="flex items-center justify-between gap-2 h-7">
        <span className="font-mono truncate">{current}</span>
        <div className="flex items-center gap-2 flex-shrink-0">
          {/* Only the watcher is marked. The full agent is the default and
              carries no pill: a badge on every ordinary node says nothing and
              costs a row's worth of attention on every node in the fleet — the
              pill is worth reading precisely because it is rare. */}
          {kind === 'watcher' && (
            <span
              className="text-xxs uppercase tracking-wide rounded border border-blue-6 text-blue-6 px-1"
              title={t('widgets.sidebars.node.agentVersionBadge.watcherTitle')}
            >
              {t('widgets.sidebars.node.agentVersionBadge.watcher')}
            </span>
          )}
          {kind !== 'watcher' && upToDate ? (
            <span className="text-xxs uppercase tracking-wide text-green-6">
              {t('widgets.sidebars.node.agentVersionBadge.upToDate')}
            </span>
          ) : (
            kind !== 'watcher' &&
            offerUpdate &&
            canUpdate && (
              <Button
                size="sm"
                kind="secondary"
                onClick={() => setConfirmOpen(true)}
                disabled={busy}
                // update_agent is queueable: for an offline node it waits for
                // the next connect, which the title says.
                title={
                  !online
                    ? t('widgets.sidebars.node.agentVersionBadge.offlineTitle')
                    : undefined
                }
              >
                {busy
                  ? '…'
                  : t('widgets.sidebars.node.agentVersionBadge.update')}
              </Button>
            )
          )}
        </div>
      </div>

      <ConfirmDialog
        open={confirmOpen}
        title={t('widgets.sidebars.node.agentVersionBadge.confirmTitle')}
        message={t('widgets.sidebars.node.agentVersionBadge.confirmMessage', {
          versionSuffix: latest ? ` (${latest})` : '',
        })}
        confirmText={t('widgets.sidebars.node.agentVersionBadge.update')}
        busy={busy}
        onConfirm={handleUpdate}
        onCancel={() => setConfirmOpen(false)}
      />
    </InfoRow>
  )
}
