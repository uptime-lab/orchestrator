// Note: after editing ViteConfig type we should also update Dockerfile that puts runtime env variables into JS
export type ViteConfig = {
  VITE_API_URL?: string
  VITE_WEBSOCKET_URL?: string
  VITE_ESSENTIALS_API_ONLY?: 'on' | 'off'
  // VITE_TERMS_URL is an https link to the operator's own terms of use. Set,
  // sign-up requires agreeing to it and the account menu links to it; empty or
  // not https, neither is shown. Per-install rather than a server capability:
  // the sign-up page holds no token to ask /server/capabilities with.
  VITE_TERMS_URL?: string
}

// This methods takes .env variables from Vite during development and runtime Docker envs during production
export const getConfigValue = (configKey: keyof ViteConfig) => {
  const configStorage =
    import.meta.env.MODE === 'production'
      ? window.orchestratorConfig
      : import.meta.env

  if (configStorage && configKey in configStorage) {
    return configStorage[configKey]
  }

  if (!configStorage) {
    // eslint-disable-next-line no-console
    console.error('[Config] No config is presented')
    return undefined
  }

  // eslint-disable-next-line no-console
  console.error(`[Config] Key not found in config: ${configKey}`)
}

// Declare global window type
declare global {
  interface Window {
    orchestratorConfig: ViteConfig
  }
}

// Initialize global config
window.orchestratorConfig = window.orchestratorConfig || {}
