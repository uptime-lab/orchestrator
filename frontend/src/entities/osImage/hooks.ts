import { useStoreMap, useUnit } from 'effector-react'

import { ImageResult } from '@/entities/osImage/api/types.ts'

import { $images, $imagesById } from './model'

export const useImages = () => {
  const images = useUnit($images)

  return { images }
}

export const useImage = (params: { imageId: string }) => {
  const image: ImageResult | null = useStoreMap({
    store: $imagesById,
    keys: [params.imageId],
    fn: (imagesById, [imageId]) => imagesById[imageId] ?? null,
  })

  return { image }
}
