import { getInvitePreview, postAcceptInvite } from './acceptInvite.ts'
import { getAccounts, postSwitchAccount } from './accounts.ts'
import { postResendVerification, postVerifyEmail } from './emailVerification.ts'
import { postLogout } from './logout.ts'
import {
  completeOidcLogin,
  linkExistingAccount,
  startOidcLogin,
} from './oidc.ts'
import {
  deletePasskey,
  isPasskeySupported,
  listPasskeys,
  loginWithPasskey,
  registerPasskey,
} from './passkey.ts'
import { postForgotPassword, postResetPassword } from './passwordReset.ts'
import { postInviteSignup } from './postInviteSignup.ts'
import { postRefreshToken } from './postRefreshToken.ts'
import { postSelectAccount } from './postSelectAccount.ts'
import { postSignIn } from './postSignIn.ts'
import { postSignUp } from './postSignUp.ts'
import {
  deleteTwoFactor,
  getTwoFactorStatus,
  postCompleteTwoFactor,
  postConfirmTwoFactor,
  postEnrollTwoFactor,
} from './twoFactor.ts'

export const apiAuth = {
  postSignIn,
  postSignUp,
  postRefreshToken,
  postSelectAccount,
  postInviteSignup,
  postAcceptInvite,
  getInvitePreview,
  postLogout,
  getAccounts,
  postSwitchAccount,
  startOidcLogin,
  completeOidcLogin,
  linkExistingAccount,
  postVerifyEmail,
  postResendVerification,
  postForgotPassword,
  postResetPassword,
  getTwoFactorStatus,
  postEnrollTwoFactor,
  postConfirmTwoFactor,
  deleteTwoFactor,
  postCompleteTwoFactor,
  listPasskeys,
  registerPasskey,
  deletePasskey,
  loginWithPasskey,
  isPasskeySupported,
}
