import type { TwoFactorMethod } from './api/postSignIn.ts'

// normalizeTwoFactorMethods decides what the second-factor step may offer.
//
// A server that predates `two_factor_methods` (a backend still rolling while
// the SPA is already new) sends none; the only factor such a server knows is a
// code. An EMPTY list is treated the same way rather than rendering a bare
// title: the backend cannot emit one today, and a step that offers nothing is
// a dead end the user cannot leave.
export const normalizeTwoFactorMethods = (
  methods: TwoFactorMethod[] | null | undefined,
): TwoFactorMethod[] => (methods && methods.length > 0 ? methods : ['totp'])
