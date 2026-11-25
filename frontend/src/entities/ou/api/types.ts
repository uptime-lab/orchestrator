import type { LEDConfig } from '@/entities/node/api/led'
import type { WatchdogConfig } from '@/entities/node/api/watchdog'

// Tri-state policy flag. `inherit` means the value is resolved from a parent
// scope (parent OU, then account default).
export type Flag = 'inherit' | 'on' | 'off'

export type OU = {
  id: string
  account_id: string
  name: string
  parent_id?: string | null
  is_root: boolean
  position: number
  network_first: Flag
  boot_to_os: Flag
  // Whether sshd accepts password logins. Named in the positive: `off`
  // unambiguously means passwords are refused.
  ssh_password_auth: Flag
  // Inheritable provisioning defaults (empty/omitted = inherit)
  default_username?: string
  has_default_password: boolean
  ssh_key_id?: string
  os_image_id?: string
  // Inheritable hardware equipage (omitted = inherit). One block: profile +
  // factory variant + plugged modules together.
  device_profile?: OUDeviceProfile
  // Inheritable LED behaviour (omitted = inherit). One block, like the
  // device profile.
  led_config?: LEDConfig
  // Inheritable watchdog supervision (omitted = inherit). Declared state
  // only; a device receives it through its per-node apply.
  watchdog_config?: WatchdogConfig
  created_at: string
  updated_at: string
}

export type OUDeviceProfile = {
  profile_id: string
  variant_id?: string
  modules?: { module_id: string; port_id: string }[]
}

export type OUListResult = {
  units: OU[]
  count: number
}

export type CreateOURequest = {
  name: string
  parent_id?: string
  network_first?: Flag
  boot_to_os?: Flag
  ssh_password_auth?: Flag
  // Provisioning defaults (empty string / omitted = inherit)
  default_username?: string
  default_password?: string
  ssh_key_id?: string
  os_image_id?: string
}

export type UpdateOURequest = {
  name?: string
  // Reparent: omitted = unchanged, "" = make root, <root uuid> = nest (2 levels max).
  parent_id?: string
  // Sibling sort order.
  position?: number
  network_first?: Flag
  boot_to_os?: Flag
  ssh_password_auth?: Flag
  // Provisioning defaults: omitted = unchanged, "" = clear to inherit, value = set
  default_username?: string
  default_password?: string
  ssh_key_id?: string
  os_image_id?: string
  // Hardware equipage: omitted = unchanged, empty profile_id = clear to
  // inherit, populated = set for the unit's nodes.
  device_profile?: OUDeviceProfile
  // LED behaviour: omitted = unchanged, all-defaults = clear to inherit.
  led_config?: LEDConfig
  // Watchdog supervision: omitted = unchanged, a disabled config = clear to
  // inherit (a declaration is always an armed supervision).
  watchdog_config?: WatchdogConfig
}

export type AssignNodeOURequest = {
  ou_id: string | null
}

export type SetNodePolicyRequest = {
  network_first?: Flag
  boot_to_os?: Flag
  ssh_password_auth?: Flag
}

// A single resolved policy value together with the scope it came from.
export type EffectivePolicyValue = {
  value: boolean
  source: string
}

// A resolved provisioning default: `present` indicates whether any scope set it.
export type EffectiveProvisioningValue<T> = {
  value: T
  present: boolean
  source: string
}

export type EffectivePolicyResult = {
  network_first: EffectivePolicyValue
  boot_to_os: EffectivePolicyValue
  ssh_password_auth: EffectivePolicyValue
  default_username: EffectiveProvisioningValue<string>
  // Password value is always empty/masked; only `present` and `source` are meaningful.
  default_password: EffectiveProvisioningValue<string>
  ssh_key_id: EffectiveProvisioningValue<string | null>
  os_image_id: EffectiveProvisioningValue<string | null>
}
