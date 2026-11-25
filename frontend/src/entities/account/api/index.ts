import {
  deleteAccountDeletionRequest,
  getAccountDeletionRequest,
  postAccountDeletionRequest,
} from './accountDeletion.ts'
import { getAccount } from './getAccount.ts'
import { getAccountExport } from './getAccountExport.ts'
import {
  putAccountIdentifyDuration,
  putAccountTwoFactorPolicy,
  putRenameAccount,
} from './putRenameAccount.ts'

export const apiAccount = {
  getAccount,
  putRenameAccount,
  putAccountTwoFactorPolicy,
  putAccountIdentifyDuration,
  getAccountExport,
  postAccountDeletionRequest,
  getAccountDeletionRequest,
  deleteAccountDeletionRequest,
}

export type { AccountDeletionRequest } from './accountDeletion.ts'
export type { Account } from './types.ts'
export { ACCOUNT_NAME_MAX, ACCOUNT_NAME_MIN } from './types.ts'
