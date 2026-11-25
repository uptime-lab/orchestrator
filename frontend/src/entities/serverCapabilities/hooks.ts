import { useUnit } from 'effector-react'

import { $serverCapabilities } from './model'

// useServerCapabilities returns the deployment's capability flags. Defaults to
// all-enabled until the map loads, so UI gating fails open (same behavior as
// before capabilities existed).
export const useServerCapabilities = () => useUnit($serverCapabilities)
