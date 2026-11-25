import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useImages } from '../../hooks'
import { imageColumns } from './imageColumns'
import { ImageRow, ImageRowLocator } from './imageRow'

export type ImageListLocator = Locator<{
  card: ImageRowLocator
}>

type ImageSort = {
  key: string
  dir: 'asc' | 'desc'
}

export type ImageListProps = {
  selectedImageId?: string
  onImageClick?: (id: string) => unknown
  // When set, only these images are rendered (rail OU filtering).
  visibleIds?: string[]
  // Image ids that are some OU's default install image (drives the badge).
  defaultForOUs?: Set<string>
  // Image id the account policy names as its default install image.
  accountDefaultId?: string
  // Empty-state text; the Operations tabs pass kind-specific wording (a
  // backups tab must not tell the operator to upload an image).
  emptyText?: string
} & Partial<Mark<ImageListLocator>>

export const ImageList = ({
  selectedImageId,
  onImageClick,
  visibleIds,
  defaultForOUs,
  accountDefaultId,
  emptyText,
  ...rest
}: ImageListProps) => {
  const { t } = useTranslation()
  const { images: allImages } = useImages()
  const images = visibleIds
    ? allImages.filter((img) => visibleIds.includes(img.id))
    : allImages
  const locator = createLocator(rest as Mark<ImageListLocator>)
  const [sort, setSort] = useState<ImageSort | null>(null)

  const toggleSort = (key: string) => {
    setSort((prev) => {
      if (prev?.key !== key) return { key, dir: 'asc' }
      if (prev.dir === 'asc') return { key, dir: 'desc' }
      return null
    })
  }

  const sortedImages = useMemo(() => {
    if (!sort) return images
    const column =
      sort.key === 'title'
        ? { sortValue: (img: (typeof images)[number]) => img.title ?? '' }
        : imageColumns().find((c) => c.key === sort.key)
    if (!column) return images
    const factor = sort.dir === 'asc' ? 1 : -1
    return [...images].sort((a, b) => {
      const va = column.sortValue(a)
      const vb = column.sortValue(b)
      if (typeof va === 'number' && typeof vb === 'number') {
        return (va - vb) * factor
      }
      return String(va).localeCompare(String(vb)) * factor
    })
  }, [images, sort])

  // A header label that toggles the column's sort (asc -> desc -> off),
  // same interaction as the node table.
  const sortHeader = (key: string, label: string) => (
    <button
      type="button"
      onClick={() => toggleSort(key)}
      className={clsx(
        'flex items-center gap-1 uppercase tracking-wide transition-colors',
        sort?.key === key
          ? 'text-lime-6'
          : 'text-neutral-7 hover:text-neutral-4',
      )}
    >
      {label}
      <span className="w-2">
        {sort?.key === key ? (sort.dir === 'asc' ? '↑' : '↓') : ''}
      </span>
    </button>
  )

  if (images.length === 0) {
    return (
      <div className="text-center text-neutral-4 py-12">
        {emptyText ?? t('entities.osImage.imageList.empty')}
      </div>
    )
  }

  return (
    // @container: column visibility responds to the list width (the details
    // panel shrinks it), matching the node table behaviour.
    <div className="@container flex flex-col gap-2" {...locator()}>
      <div
        className={clsx(
          'hidden @[30rem]:flex items-center gap-4 px-4 pb-1',
          'text-xxs uppercase tracking-wide text-neutral-7 select-none',
        )}
      >
        <div className="w-64 flex-shrink-0 pl-11">
          {sortHeader('title', t('entities.osImage.imageList.columnImage'))}
        </div>
        <div className="flex items-center gap-6 flex-1 min-w-0">
          {imageColumns().map((column) => (
            <div key={column.key} className={column.className}>
              {sortHeader(column.key, column.label)}
            </div>
          ))}
        </div>
      </div>

      {sortedImages.map((image) => (
        <ImageRow
          key={image.id}
          id={image.id}
          image={image}
          selected={selectedImageId === image.id}
          isDefaultForOU={defaultForOUs?.has(image.id) ?? false}
          isAccountDefault={!!accountDefaultId && accountDefaultId === image.id}
          onClick={onImageClick}
          {...locator.card()}
        />
      ))}
    </div>
  )
}
