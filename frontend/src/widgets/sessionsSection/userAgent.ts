// describeUserAgent turns a session's User-Agent header into "Browser on
// OS". A string it does not recognise yields null, and the list shows a
// readable fallback with the raw string behind a tooltip. The order of the
// browser checks matters: Edge and Opera also say "Chrome", Chrome also
// says "Safari".
export type UserAgentDescription = { browser: string; os: string | null }

const BROWSERS: [RegExp, string][] = [
  [/\bEdg(e|A|iOS)?\//, 'Edge'],
  [/\b(OPR|Opera)\//, 'Opera'],
  [/\bFirefox\/|\bFxiOS\//, 'Firefox'],
  [/\b(Chrome|CriOS|Chromium)\//, 'Chrome'],
  [/\bVersion\/[\d.]+.*\bSafari\//, 'Safari'],
]

const SYSTEMS: [RegExp, string][] = [
  [/\b(iPhone|iPad|iPod)\b/, 'iOS'],
  [/\bAndroid\b/, 'Android'],
  [/\bCrOS\b/, 'ChromeOS'],
  [/\bWindows\b/, 'Windows'],
  [/\bMac OS X\b|\bMacintosh\b/, 'macOS'],
  [/\bLinux\b/, 'Linux'],
]

export const describeUserAgent = (
  ua: string | null | undefined,
): UserAgentDescription | null => {
  if (!ua) return null
  const browser = BROWSERS.find(([re]) => re.test(ua))?.[1]
  if (!browser) return null
  const os = SYSTEMS.find(([re]) => re.test(ua))?.[1] ?? null
  return { browser, os }
}
