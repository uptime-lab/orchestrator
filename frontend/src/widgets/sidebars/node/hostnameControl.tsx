import { FC, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiNodes } from '@/entities/node/api'
import { fetchNodeCommandsFx } from '@/entities/node/model'

import { Button } from '@/shared/ui/button/button.tsx'

import { SubViewHeader } from './subViewHeader.tsx'

// RFC 1123 hostname label: lowercase letters, digits, hyphens; no leading or
// trailing hyphen; up to 63 chars.
const HOSTNAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

// Renew the DHCP lease so the device re-announces its (new) hostname to the
// DHCP server (option 12) → dynamic DNS, without waiting for the lease to
// expire. Targets the default-route interface via systemd-networkd (the managed
// Ubuntu stack), falling back to a networkd restart, then dhclient.
// Marker comment on line 2 of the renew script so the command history can label
// this run_script as "Refresh DHCP lease" instead of dumping the raw shell.
export const DHCP_RENEW_MARKER = 'orchestrator-action: dhcp-renew'

const DHCP_RENEW_SCRIPT = `#!/bin/sh
# ${DHCP_RENEW_MARKER}
IFACE=$(ip route show default 2>/dev/null | awk '{print $5; exit}')
if [ -n "$IFACE" ] && command -v networkctl >/dev/null 2>&1; then
  networkctl renew "$IFACE"
elif command -v systemctl >/dev/null 2>&1; then
  systemctl restart systemd-networkd
elif command -v dhclient >/dev/null 2>&1; then
  dhclient -r "$IFACE" 2>/dev/null; dhclient "$IFACE"
fi
`

// HostnameControl is the inline Actions-tab sub-view for renaming a node. It
// applies over the agent's set_hostname command (hostnamectl) — live, no reboot
// — but DHCP/DNS infrastructure only learns the new name on the next lease
// renewal, so the warning below sets that expectation.
export const HostnameControl: FC<{
  nodeId: string
  hostname?: string
  online: boolean
  // Whether the viewer may run scripts on the node (run_script → NodeFileWrite);
  // gates the "Refresh DHCP lease" affordance.
  canRefreshDhcp: boolean
  onBack: () => void
}> = ({ nodeId, hostname, online, canRefreshDhcp, onBack }) => {
  const { t } = useTranslation()
  const [value, setValue] = useState(hostname ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dhcpBusy, setDhcpBusy] = useState(false)
  const [dhcpDone, setDhcpDone] = useState(false)
  const [saved, setSaved] = useState(false)
  const [queued, setQueued] = useState(false)

  const current =
    hostname && hostname.trim()
      ? hostname
      : t('widgets.sidebars.node.hostnameControl.unknown')
  const valid = HOSTNAME_RE.test(value)
  const unchanged = value === (hostname ?? '')

  const handleSave = async () => {
    if (!valid) return
    setBusy(true)
    setError(null)
    try {
      const accepted = await apiNodes.setHostname(nodeId, value)
      setQueued(accepted.delivery === 'queued')
      await fetchNodeCommandsFx(nodeId)
      // Stay on the sub-view so the operator can then refresh the DHCP lease;
      // returning is an explicit "← Back".
      setSaved(true)
    } catch {
      setError(t('widgets.sidebars.node.hostnameControl.sendError'))
    } finally {
      setBusy(false)
    }
  }

  const handleRefreshDhcp = async () => {
    setDhcpBusy(true)
    setError(null)
    try {
      await apiNodes.postNodeCommand(nodeId, {
        command: 'run_script',
        params: { script: DHCP_RENEW_SCRIPT, timeout_seconds: 60 },
      })
      await fetchNodeCommandsFx(nodeId)
      setDhcpDone(true)
      setTimeout(() => setDhcpDone(false), 3000)
    } catch {
      setError(t('widgets.sidebars.node.hostnameControl.dhcpError'))
    } finally {
      setDhcpBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <SubViewHeader
        title={t('widgets.sidebars.node.hostnameControl.title')}
        onBack={onBack}
      />

      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.node.hostnameControl.current')}{' '}
        <span className="font-mono text-lime-6">{current}</span>
      </div>

      <div className="flex flex-col gap-2">
        <label className="text-xs font-medium text-neutral-7 uppercase tracking-wide">
          {t('widgets.sidebars.node.hostnameControl.newHostnameLabel')}
        </label>
        <input
          value={value}
          onChange={(e) => {
            setValue(e.target.value.toLowerCase())
            setSaved(false)
          }}
          autoFocus
          spellCheck={false}
          maxLength={63}
          className="w-full h-9 rounded border border-neutral-11 bg-neutral-13 px-3 font-mono text-sm text-lime-6 focus:outline-none focus:border-lime-8"
        />
        {(error || (!valid && value.length > 0)) && (
          <span className="text-xxs text-red-5">
            {error ?? t('widgets.sidebars.node.hostnameControl.invalidHint')}
          </span>
        )}
      </div>

      {/* Buttons first, same size (both md, full-width); explanatory messages
          go below so they never push the actions around. */}
      <Button
        size="md"
        onClick={handleSave}
        // set_hostname is queueable: an offline node takes it on its next
        // connect, and the queued notice below says so.
        disabled={busy || !valid || unchanged || saved}
        grow
      >
        {busy
          ? t('widgets.sidebars.node.hostnameControl.saving')
          : saved
            ? t('widgets.sidebars.node.hostnameControl.saved')
            : t('widgets.sidebars.node.hostnameControl.save')}
      </Button>

      {canRefreshDhcp && (
        <Button
          size="md"
          kind="secondary"
          onClick={handleRefreshDhcp}
          disabled={!online || dhcpBusy}
          title={
            online
              ? t('widgets.sidebars.node.hostnameControl.dhcpTitle')
              : t('widgets.sidebars.node.hostnameControl.nodeMustBeOnline')
          }
          grow
        >
          {dhcpBusy
            ? t('widgets.sidebars.node.hostnameControl.dhcpRequesting')
            : dhcpDone
              ? t('widgets.sidebars.node.hostnameControl.dhcpRequested')
              : t('widgets.sidebars.node.hostnameControl.refreshDhcp')}
        </Button>
      )}

      {saved && !error && (
        <div className="rounded border border-lime-6/30 bg-lime-6/10 px-3 py-2 text-xs text-lime-6">
          {queued
            ? t('widgets.sidebars.node.hostnameControl.queuedNotice')
            : t('widgets.sidebars.node.hostnameControl.savedNotice')}
        </div>
      )}

      <div className="rounded border border-amber-6/40 bg-amber-6/10 px-3 py-2 text-xs text-neutral-4">
        {t('widgets.sidebars.node.hostnameControl.warningBefore')}
        <span className="text-neutral-2">
          {t('widgets.sidebars.node.hostnameControl.warningNoReboot')}
        </span>
        {t('widgets.sidebars.node.hostnameControl.warningAfter')}
        {canRefreshDhcp &&
          t('widgets.sidebars.node.hostnameControl.warningDhcpHint')}
      </div>
    </div>
  )
}
