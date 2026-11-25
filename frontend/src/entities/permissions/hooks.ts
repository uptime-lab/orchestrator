import { useUnit } from 'effector-react'

import { $permissions, can, canAnyScope } from './model'

// useCan returns a checker `(action, ouId?) => boolean` over the current
// permission map. A single hook call gates any number of UI elements (menus,
// per-row actions) without violating the rules of hooks.
export const useCan = () => {
  const perms = useUnit($permissions)
  return (action: string, ouId?: string | null) => can(perms, action, ouId)
}

// useCanAnyScope returns a checker `(action) => boolean` that asks whether the
// user holds an action in ANY scope. For navigation only — see canAnyScope.
export const useCanAnyScope = () => {
  const perms = useUnit($permissions)
  return (action: string) => canAnyScope(perms, action)
}

// The action names live in ./actions so a module that needs only a string
// does not import the store graph. Re-exported here for the many call sites
// that take both from this module.
export { Actions } from './actions'
