import { createEffect, createStore, sample } from 'effector'
import { t } from 'i18next'

import { authModel } from '@/entities/auth'
import { AuthStatus } from '@/entities/auth/types'

import { showError } from '@/shared/ui/toast'

import { getMyPermissions, PermissionMap } from './api'
import { shouldRefreshPermissions } from './refreshPolicy'

// $permissions holds the current user's computed permission map for the
// active account (empty until loaded / when logged out). The backend remains
// the source of truth; this only drives UI gating.
export const $permissions = createStore<PermissionMap>({})

export const loadPermissionsFx = createEffect<void, PermissionMap>(async () => {
  return getMyPermissions()
})

export const resetPermissions = createEffect<void, PermissionMap>(
  async () => ({}),
)

// $permissionsFailed distinguishes "this user is allowed nothing" from "we
// could not find out". Both leave $permissions empty and therefore hide every
// action, which is the correct FAIL-CLOSED behaviour and is deliberately NOT
// changed here — can() still answers false. What was missing is that the two
// look identical on screen: a failed load renders a fully drawn app with every
// button gone and no explanation, recoverable only by guessing at a reload.
//
// Read this flag to SAY something (a banner, a retry). Never to grant: gating
// stays with can(), so a bug here cannot open an action up.
export const $permissionsFailed = createStore(false)

$permissions
  .on(loadPermissionsFx.doneData, (_, perms) => perms)
  .on(loadPermissionsFx.fail, () => ({}))
  .reset(resetPermissions.done)

$permissionsFailed
  .on(loadPermissionsFx.fail, () => true)
  .on(loadPermissionsFx.doneData, () => false)
  .reset(resetPermissions.done)

// Say it out loud as well. Error toasts do not auto-dismiss, so this stays on
// screen next to an app whose buttons have all vanished — which is the only
// thing that turns "the product is broken" into "one request failed, reload".
loadPermissionsFx.fail.watch(({ error }) => {
  showError(t('entities.permissions.loadFailed'), error)
})

// Load the permission map on sign-in (login, account select/switch, init) and
// clear it on sign-out.
sample({ clock: authModel.events.signIn, target: loadPermissionsFx })
sample({ clock: authModel.events.signOut, target: resetPermissions })

// A quiet re-read: on success the map is replaced; a failure keeps the map
// the tab already has (and says nothing), because a missed refresh is no
// reason to hide every control the way a failed first load must.
export const refreshPermissionsFx = createEffect<void, PermissionMap>(
  async () => getMyPermissions(),
)
$permissions.on(refreshPermissionsFx.doneData, (_, perms) => perms)

let lastLoadedAt: number | null = null
loadPermissionsFx.done.watch(() => {
  lastLoadedAt = Date.now()
})
refreshPermissionsFx.done.watch(() => {
  lastLoadedAt = Date.now()
})
authModel.events.signOut.watch(() => {
  lastLoadedAt = null
})

// Re-read when the tab comes back into view, so a role or permission change
// made elsewhere shows without a new sign-in (see refreshPolicy).
export const refreshPermissionsIfStale = () => {
  const signedIn =
    authModel.$authState.getState().status === AuthStatus.LoggedIn
  if (!shouldRefreshPermissions(lastLoadedAt, Date.now(), signedIn)) return
  if (refreshPermissionsFx.pending.getState()) return
  void refreshPermissionsFx().catch(() => undefined)
}
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') refreshPermissionsIfStale()
  })
}

// The two predicates live in ./can — pure, and therefore importable by a test
// without pulling in this file's store graph. Re-exported here so every existing
// caller keeps its import path.
export { can, canAnyScope } from './can'
