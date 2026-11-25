// buildInstallCommand renders the one-command agent installer for an adoption
// token. The token is a base64(JSON) blob that already carries platform_url —
// use it as the script origin so the command works no matter what origin the
// UI itself is served from (e.g. an SSH tunnel on-prem).
export const buildInstallCommand = (adoptionBlob: string): string => {
  let platform = window.location.origin
  try {
    const decoded = JSON.parse(atob(adoptionBlob.trim())) as {
      platform_url?: string
    }
    if (decoded.platform_url) platform = decoded.platform_url.replace(/\/$/, '')
  } catch {
    // Not decodable — fall back to the UI origin.
  }
  // The script refuses a plaintext platform URL unless told the operator
  // means it. An install that advertises http:// has already declared that
  // (it is a server-side setting), so the command it shows must run as
  // shown instead of failing on the device.
  const insecure = platform.startsWith('http://') ? ' --insecure' : ''
  // --token argument form: `sudo VAR=x sh` needs SETENV in sudoers, which
  // restricted configurations lack; the argument always passes through. The
  // blob is base64 (A-Za-z0-9+/=), so single-quoting is always safe.
  return `curl -fsSL ${platform}/install.sh | sudo sh -s -- --token '${adoptionBlob.trim()}'${insecure}`
}
