import { createEvent, createStore } from 'effector'
import { useUnit } from 'effector-react'

// Per-user display preferences. Stored client-side for now; migrates into
// the user profile once the broader auth/users/teams work lands.
const PREFER_HOSTNAMES_KEY = 'pref-hostnames'

export const $preferHostnames = createStore<boolean>(
  localStorage.getItem(PREFER_HOSTNAMES_KEY) === '1',
)

export const setPreferHostnames = createEvent<boolean>()

$preferHostnames.on(setPreferHostnames, (_, value) => value)
$preferHostnames.watch((value) => {
  localStorage.setItem(PREFER_HOSTNAMES_KEY, value ? '1' : '0')
})

export const usePreferHostnames = (): boolean => useUnit($preferHostnames)
