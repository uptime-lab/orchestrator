// dataTransfer key for dragging a utility (a catalog entry) onto an OU in the
// rail — the drop CREATES an assignment for that unit and opens the config
// sidebar. It lives in the entity rather than the utilities route for the same
// reason as SECRET_TAG_DND_TYPE: the drop target is
// configured in the route while drag sources can multiply, and a key only the
// route knows strands every other source.
export const UTILITY_DND_TYPE = 'text/utility-id'

// dataTransfer key for dragging an existing ASSIGNMENT row onto the rail —
// the drop TOGGLES the unit in the assignment's OU set (add if absent, remove
// if present; the workflows drag-to-scope precedent), while a catalog drop
// above CREATES a new assignment. Two keys because the same rail must tell the
// two gestures apart.
export const UTILITY_ASSIGNMENT_DND_TYPE = 'text/utility-assignment-id'
