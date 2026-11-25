import { deleteImage } from './deleteImage.ts'
import { getImage } from './getImage.ts'
import { getImageDownload } from './getImageDownload.ts'
import { getImages } from './getImages.ts'
import { postCompressImage } from './postCompressImage.ts'
import { postCreateImage } from './postCreateImage.ts'
import { postUploadImageFile } from './postUploadImageFile.ts'
import { putUpdateImage } from './putUpdateImage.ts'

export const apiImages = {
  getImages,
  getImage,
  postCreateImage,
  putUpdateImage,
  deleteImage,
  postUploadImageFile,
  postCompressImage,
  getImageDownload,
}
