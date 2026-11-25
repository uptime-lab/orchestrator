// Default runtime config. In production the container's startup script
// OVERWRITES this file with the real API_URL/WEBSOCKET_URL env values; in the
// Vite dev server this default is served as-is and getConfigValue() falls back
// to import.meta.env anyway (dev mode reads .env, not this object).
window.orchestratorConfig = {
  VITE_API_URL: '',
  VITE_WEBSOCKET_URL: '',
  VITE_ESSENTIALS_API_ONLY: 'off',
  VITE_TERMS_URL: '',
}
