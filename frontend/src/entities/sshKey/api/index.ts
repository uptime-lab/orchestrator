import { deleteSSHKey } from './deleteSSHKey.ts'
import { getSSHKey } from './getSSHKey.ts'
import {
  downloadSSHKeyPublicKey,
  getSSHKeyPublicKeyText,
} from './getSSHKeyPublicKey.ts'
import { getSSHKeys } from './getSSHKeys.ts'
import { postCreateSSHKey } from './postCreateSSHKey.ts'
import { generateSSHKey } from './postGenerateSSHKey.ts'
import { putUpdateSSHKey } from './putUpdateSSHKey.ts'

export const apiSSHKeys = {
  getSSHKeys,
  getSSHKey,
  postCreateSSHKey,
  generateSSHKey,
  downloadSSHKeyPublicKey,
  getSSHKeyPublicKeyText,
  putUpdateSSHKey,
  deleteSSHKey,
}
