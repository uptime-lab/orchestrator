import type { AxiosError } from 'axios'

export const AUTH_LOCAL_STORAGE_EMAIL_KEY = 'auth_email'

// we don't have full set of http codes on BE yet, so we have to make a workaround to detect bad auth
export const isAxiosErrorBadAuth = (error: AxiosError) =>
  ['record not found', 'authentification failed, try again'].includes(
    String(error.response?.data),
  )
export const delay = (ms: number) =>
  new Promise((resolve) => setTimeout(resolve, ms))
