import { getConfigValue } from '@/shared/utils/config'

import { parseTermsUrl } from './parse'

// getTermsUrl is the operator's terms-of-use link (TERMS_URL in the frontend
// container's environment), or null when this install has none. A runtime
// setting rather than a server capability: the sign-up page must know it, and
// there is no token there to ask /server/capabilities with.
export const getTermsUrl = (): string | null =>
  parseTermsUrl(getConfigValue('VITE_TERMS_URL'))
