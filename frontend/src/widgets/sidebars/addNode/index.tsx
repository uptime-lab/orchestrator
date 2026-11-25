import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { t } from 'i18next'
import { FC, useEffect } from 'react'

import { SidebarEnum } from '@/widgets/sidebars/types'

import { apiNodes } from '@/entities/node/api'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { useServerCapabilities } from '@/entities/serverCapabilities'
import { userCommonSocketModel } from '@/entities/userCommonSocket'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'
import { refusalOutlook, visibleRefusals } from './refusalOutlook'
import { CommandTab } from './tabs/CommandTab'
import { MasterImageTab } from './tabs/MasterImageTab'
import { SingleNodeTab } from './tabs/SingleNodeTab'
import { SSHTab } from './tabs/SSHTab'
import { USBFlashTab } from './tabs/USBFlashTab'

// AddNodeTab is encoded in the shared `view` search param so a tab is
// deep-linkable and survives the back button (the default tab omits it).
const TABS = ['single', 'command', 'usb', 'ssh', 'master'] as const
export type AddNodeTab = (typeof TABS)[number]

// Resolved lazily: calling t() while the module is evaluated would freeze the
// labels before i18next has its resources.
const tabLabel = (tab: AddNodeTab): string => {
  switch (tab) {
    case 'single':
      return t('widgets.sidebars.addNode.tabs.single')
    case 'command':
      return t('widgets.sidebars.addNode.tabs.command')
    case 'usb':
      return t('widgets.sidebars.addNode.tabs.usb')
    case 'ssh':
      return t('widgets.sidebars.addNode.tabs.ssh')
    case 'master':
      return t('widgets.sidebars.addNode.tabs.master')
  }
}

// AdoptionRefusedNotice lists the devices the server has turned away.
//
// The device itself cannot tell anyone: it gets a deliberately vague gRPC error
// (the refusal concerns another tenant's state) and simply retries. Before
// this, the only symptom was that nothing ever appeared — indistinguishable
// from the install command not working. The list is the platform's own refusal
// record, read on open, so a refusal that happened while this sidebar was
// closed is still here; the WebSocket notice only makes it refresh sooner. It
// polls while mounted so `live` goes out when the device does.
const AdoptionRefusedNotice: FC = () => {
  const queryClient = useQueryClient()
  const { data } = useQuery({
    queryKey: ['adoption-refusals'],
    queryFn: apiNodes.getAdoptionRefusals,
    refetchInterval: 30_000,
  })

  useEffect(() => {
    const unsubscribe =
      userCommonSocketModel.events.userSocketOnNodeAdoptionRefused.watch(
        () =>
          void queryClient.invalidateQueries({
            queryKey: ['adoption-refusals'],
          }),
      )
    return () => unsubscribe()
  }, [queryClient])

  const rows = visibleRefusals(data ?? [])
  if (rows.length === 0) return null

  return (
    <div className="flex flex-col gap-2">
      {rows.map((r) => {
        const outlook = refusalOutlook(r)
        return (
          <div
            key={r.hardware_serial}
            className={
              'flex flex-col gap-1 rounded border bg-neutral-11 p-3 ' +
              (outlook.tone === 'error' ? 'border-red-6' : 'border-orange-5')
            }
          >
            <div
              className={
                'text-sm ' +
                (outlook.tone === 'error' ? 'text-red-6' : 'text-orange-5')
              }
            >
              {t('widgets.addNode.refused.title', {
                serial: r.hardware_serial,
              })}
            </div>
            <div className="text-xs text-neutral-7">{outlook.text}</div>
            <div className="font-mono text-xxs text-neutral-6">
              {t('widgets.addNode.refused.retries', {
                count: r.occurrences,
                since: new Date(r.first_seen_at).toLocaleString(),
              })}
            </div>
          </div>
        )
      })}
    </div>
  )
}

export const SidebarAddNode: FC<SidebarProps> = ({ onClose }) => {
  const navigate = useNavigate()
  const search = useSearch({ from: '/_mainLayout' })
  const caps = useServerCapabilities()
  const check = useCan()

  // Deployment-capability gating: hide every tab whose service this install
  // does not run. SSH-adopt needs the server on the devices' network (a hosted
  // install whose devices sit behind customer NAT has none), WebUSB flashing
  // needs the EEPROM signer, SD bundles the provisioning payload. The command
  // tab also needs the master-token permission (it mints one).
  const visibleTabs = TABS.filter((tab) => {
    if (tab === 'ssh') return caps.ssh_adopt
    if (tab === 'usb') return caps.eeprom_signer
    if (tab === 'master') return caps.sd_provisioning
    if (tab === 'command')
      return caps.one_command_adopt && check(Actions.MasterTokenManage)
    return true
  })

  const active: AddNodeTab = visibleTabs.includes(search.view as AddNodeTab)
    ? (search.view as AddNodeTab)
    : 'single'

  const setTab = (tab: AddNodeTab) => {
    navigate({
      to: '.',
      search: (old) => ({
        ...old,
        sidebar: SidebarEnum.CreateNode,
        // The default tab omits `view`, mirroring the node sidebar convention.
        view: tab === 'single' ? undefined : tab,
      }),
      replace: true,
    })
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-5">
      <SidebarCloseButton onClose={onClose} />

      <h2 className="text-lg font-bold uppercase">
        {t('widgets.sidebars.addNode.title')}
      </h2>

      <AdoptionRefusedNotice />

      <div className="flex gap-1 border-b border-neutral-11">
        {visibleTabs.map((tab) => (
          <button
            key={tab}
            type="button"
            onClick={() => setTab(tab)}
            className={
              'px-3 py-2 text-xs uppercase tracking-wide border-b-2 -mb-px transition-colors ' +
              (active === tab
                ? 'border-lime-6 text-lime-6'
                : 'border-transparent text-neutral-7 hover:text-neutral-5')
            }
          >
            {tabLabel(tab)}
          </button>
        ))}
      </div>

      {/* All tabs stay MOUNTED (hidden, not unmounted): an SSH adoption job or a
          WebUSB flash keeps its progress UI and form state across tab switches —
          conditional mounting would silently orphan the in-flight operation. */}
      <div className={active === 'single' ? undefined : 'hidden'}>
        <SingleNodeTab onClose={onClose} />
      </div>
      {visibleTabs.includes('command') && (
        <div className={active === 'command' ? undefined : 'hidden'}>
          <CommandTab />
        </div>
      )}
      {caps.eeprom_signer && (
        <div className={active === 'usb' ? undefined : 'hidden'}>
          <USBFlashTab
            onOpenSdBundle={
              caps.sd_provisioning ? () => setTab('master') : undefined
            }
          />
        </div>
      )}
      {caps.sd_provisioning && (
        <div className={active === 'master' ? undefined : 'hidden'}>
          <MasterImageTab onClose={onClose} />
        </div>
      )}
      {caps.ssh_adopt && (
        <div className={active === 'ssh' ? undefined : 'hidden'}>
          <SSHTab />
        </div>
      )}
    </div>
  )
}
