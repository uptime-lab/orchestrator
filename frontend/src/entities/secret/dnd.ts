// dataTransfer key for dragging a tag onto an OU in the rail (attaching the tag
// delivers every secret carrying it).
//
// It lives in the entity rather than in the secrets route because the drop
// TARGET is configured there while the drag SOURCES are in two different places
// — the rail's tag chips and the Tags tab's rows. When only the route knew the
// key, the Tags tab had no way to start the drop its own rail advertised, which
// read as a broken feature.
export const SECRET_TAG_DND_TYPE = 'text/secret-tag'
