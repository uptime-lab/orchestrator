import { createEffect, createStore, sample } from 'effector'

import { authModel } from '@/entities/auth'

import { getServerCapabilities, ServerCapabilities } from './api'

// Until loaded (or on fetch failure) every capability is assumed available, so
// a hiccup on this endpoint degrades to today's behavior (feature visible, its
// own API produces the error) instead of hiding working features.
const ALL_ON: ServerCapabilities = {
  ssh_adopt: true,
  eeprom_signer: true,
  netboot: true,
  sd_provisioning: true,
  terminal: true,
  one_command_adopt: true,
  boot_host: '',
}

export const $serverCapabilities = createStore<ServerCapabilities>(ALL_ON)

export const loadServerCapabilitiesFx = createEffect<void, ServerCapabilities>(
  async () => getServerCapabilities(),
)

$serverCapabilities
  .on(loadServerCapabilitiesFx.doneData, (_, caps) => caps)
  .reset(authModel.events.signOut)

// Capabilities are server-global; load them once per sign-in.
sample({ clock: authModel.events.signIn, target: loadServerCapabilitiesFx })
