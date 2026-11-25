import { useQuery } from '@tanstack/react-query'
import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import {
  getDeviceProfileCatalog,
  getNodeDeviceProfile,
} from '@/entities/deviceProfile'

import { InfoRow } from './infoRow.tsx'

// Overview CONFIGURATION row: the RESOLVED platform, read-only — Overview
// shows facts, Properties changes them (the Storage-row convention). Renders
// nothing when the node neither has a profile nor could take one, so a plain
// Pi owner's panel is unchanged.
export const PlatformRow: FC<{
  nodeId: string
  onOpenProperties: () => void
}> = ({ nodeId, onOpenProperties }) => {
  const { t } = useTranslation()
  const { data: state } = useQuery({
    queryKey: ['device-profile', nodeId],
    queryFn: () => getNodeDeviceProfile(nodeId),
  })
  const { data: catalog } = useQuery({
    queryKey: ['device-profile-catalog'],
    queryFn: getDeviceProfileCatalog,
    staleTime: 5 * 60 * 1000,
  })

  if (!state) return null
  if (!state.assigned && state.compatible_profiles.length === 0) return null

  let text = t('widgets.sidebars.node.platformRow.generic')
  let title = t('widgets.sidebars.node.platformRow.genericTitle')
  if (state.assigned) {
    const board = catalog?.boards.find((b) => b.id === state.profile_id)
    const variant = board?.variants?.find((v) => v.id === state.variant_id)
    text = [board?.name ?? state.profile_id, variant?.name]
      .filter(Boolean)
      .join(' · ')
    title =
      state.source === 'node'
        ? t('widgets.sidebars.node.platformRow.declaredTitle')
        : t('widgets.sidebars.node.platformRow.inheritedTitle')
  }

  return (
    <InfoRow label={t('widgets.sidebars.node.platformRow.label')}>
      <button
        type="button"
        onClick={onOpenProperties}
        title={title}
        className="block max-w-full truncate text-left hover:underline decoration-neutral-7 underline-offset-2"
      >
        {text}
      </button>
    </InfoRow>
  )
}
