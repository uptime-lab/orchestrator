// When a returning tab re-reads the permission map. Role and permission
// changes apply on the server at once, so a tab that loaded its map at
// sign-in kept showing controls that now answer 403 (or hiding ones just
// granted) until the next sign-in. Re-reading on every focus would be a
// request per alt-tab; at most once per interval is enough.
export const PERMISSIONS_REFRESH_INTERVAL_MS = 30_000

export const shouldRefreshPermissions = (
  lastLoadedAt: number | null,
  now: number,
  signedIn: boolean,
): boolean =>
  signedIn &&
  (lastLoadedAt === null ||
    now - lastLoadedAt >= PERMISSIONS_REFRESH_INTERVAL_MS)
