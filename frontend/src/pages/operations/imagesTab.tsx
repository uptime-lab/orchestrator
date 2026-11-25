import { useQuery } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types.ts'

import { useNodes } from '@/entities/node/hooks.ts'
import type { ImageKind } from '@/entities/osImage/api/types'
import { useImages } from '@/entities/osImage/hooks'
import { ImageList } from '@/entities/osImage/ui/imageList'
import { apiOUs, useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { useServerCapabilities } from '@/entities/serverCapabilities'

import { Button } from '@/shared/ui/button/button.tsx'

// One Operations image tab: the same inventory split by kind. Install images
// are uploaded and can be a unit's default install target; backups and gold
// images are captured from nodes, so those tabs offer no upload and no OU
// coupling — their images are anchored to a source node, not a unit.
export const OperationsImagesTab = ({ kind }: { kind: ImageKind }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as {
    sidebar?: string
    imageId?: string
    ou?: string
  }
  const canManageImages = useCan()(Actions.ImageManage)
  const { images: allImages } = useImages()
  const { ous } = useOUs()
  const { nodes } = useNodes()
  const caps = useServerCapabilities()
  // The account default install image, marked in the install list. Same
  // query key as the Organization page's account defaults, so an edit there
  // refreshes this mark.
  const { data: accountPolicy } = useQuery({
    queryKey: ['account-policy'],
    queryFn: apiOUs.getAccountPolicy,
    enabled: kind === 'os',
  })

  const kindImages = useMemo(
    () => allImages.filter((img) => (img.kind ?? 'os') === kind),
    [allImages, kind],
  )
  const atImageCap =
    !!caps.max_images_per_account &&
    allImages.length >= caps.max_images_per_account

  // The rail's OU filter means something different per kind: for install
  // images it narrows to the unit's default install image; for captures it
  // narrows to backups/gold images taken from the unit's nodes (subtree).
  const ouFilter = search.ou
  const referencedImageIds = useMemo(
    () =>
      kind === 'os'
        ? new Set(ous.map((ou) => ou.os_image_id).filter(Boolean) as string[])
        : undefined,
    [kind, ous],
  )

  const visibleImageIds = useMemo(() => {
    const kindIds = kindImages.map((img) => img.id)
    if (!ouFilter) return kindIds
    if (kind === 'os') {
      const imageId = ous.find((ou) => ou.id === ouFilter)?.os_image_id
      return imageId && kindIds.includes(imageId) ? [imageId] : []
    }
    const subtree = new Set([ouFilter])
    ous.forEach((ou) => {
      if (ou.parent_id === ouFilter) subtree.add(ou.id)
    })
    const nodeOU = new Map(nodes.map((n) => [n.id, n.ou_id || undefined]))
    return kindImages
      .filter((img) => {
        const ouId = img.source_node_id
          ? nodeOU.get(img.source_node_id)
          : undefined
        return !!ouId && subtree.has(ouId)
      })
      .map((img) => img.id)
  }, [kind, kindImages, ouFilter, ous, nodes])

  const handleImageClick = useCallback(
    (imageId: string) => {
      if (
        search.sidebar === SidebarEnum.OSImageDetails &&
        search.imageId === imageId
      ) {
        navigate({
          to: '.',
          search: (prev: Record<string, unknown>) => ({
            ...prev,
            sidebar: undefined,
            imageId: undefined,
          }),
          replace: true,
        })
        return
      }
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          sidebar: SidebarEnum.OSImageDetails,
          imageId,
        }),
      })
    },
    [navigate, search],
  )

  const handleUploadClick = useCallback(() => {
    navigate({
      to: '.',
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        sidebar: SidebarEnum.UploadOSImage,
      }),
    })
  }, [navigate])

  const selectedImageId =
    search.sidebar === SidebarEnum.OSImageDetails && search.imageId
      ? search.imageId
      : undefined

  // A unit filter that matches nothing is not an empty inventory — say which
  // it is, or the operator is told to upload an image the account already has.
  const filteredToNothing = !!ouFilter && kindImages.length > 0
  const emptyText =
    kind === 'os'
      ? filteredToNothing
        ? t('pages.operations.images.emptyForUnit')
        : t('pages.operations.images.emptyInstall')
      : kind === 'backup'
        ? filteredToNothing
          ? t('pages.operations.images.emptyBackupsForUnit')
          : t('pages.operations.images.emptyBackups')
        : filteredToNothing
          ? t('pages.operations.images.emptyGoldForUnit')
          : t('pages.operations.images.emptyGold')

  return (
    <>
      {kind === 'os' && (canManageImages || atImageCap) && (
        <div className="flex flex-wrap items-center justify-end gap-3 mb-4">
          {atImageCap && (
            <span className="text-xs text-yellow-5">
              {t('pages.operations.images.limitReached', {
                limit: caps.max_images_per_account,
              })}
            </span>
          )}
          {canManageImages && (
            <Button size="md" onClick={handleUploadClick} disabled={atImageCap}>
              {t('pages.osImages.upload')}
            </Button>
          )}
        </div>
      )}
      <ImageList
        selectedImageId={selectedImageId}
        onImageClick={handleImageClick}
        visibleIds={visibleImageIds}
        defaultForOUs={referencedImageIds}
        accountDefaultId={
          kind === 'os' ? accountPolicy?.os_image_id || undefined : undefined
        }
        emptyText={emptyText}
      />
    </>
  )
}
