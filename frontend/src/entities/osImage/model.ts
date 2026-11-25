import { createEffect, createStore, sample } from 'effector'

import { ImageResult } from '@/entities/osImage/api/types.ts'
import { userCommonSocketModel } from '@/entities/userCommonSocket'

import { deriveById } from '@/shared/utils/mergeUniqueEntities'

import { apiImages } from './api'

// ============= Store =============

export const $images = createStore<ImageResult[]>([])
export const $imagesById = $images.map(deriveById)

// ============= Effects =============

export const fetchImagesFx = createEffect(() => apiImages.getImages())
export const fetchImageFx = createEffect((id: string) => apiImages.getImage(id))

// Resync images when the socket reconnects after a drop — an image whose
// compression finished while the socket was down produced no event.
sample({
  clock: userCommonSocketModel.events.userSocketReconnected,
  target: fetchImagesFx,
})

// ============= Side effects =============

$images
  .on(fetchImagesFx.doneData, (_, images) => [...images])
  .on(fetchImageFx.doneData, (images, newImage) => {
    const existingIndex = images.findIndex((img) => img.id === newImage.id)
    if (existingIndex >= 0) {
      const updated = [...images]
      updated[existingIndex] = newImage
      return updated
    }
    return [...images, newImage]
  })
  .on(
    userCommonSocketModel.events.userSocketOnImageCreated,
    (images, { payload }) => {
      const exists = images.some((img) => img.id === payload.id)
      if (exists) {
        return images
      }
      return [...images, payload]
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnImageUpdated,
    (images, { payload }) => {
      const updatedImages = images.map((img) =>
        img.id === payload.id ? payload : img,
      )
      return updatedImages
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnImageDeleted,
    (images, { payload }) => images.filter((img) => img.id !== payload.id),
  )
