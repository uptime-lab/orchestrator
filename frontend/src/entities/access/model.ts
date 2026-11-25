import { createEvent } from 'effector'

// Fired after any access-entry mutation so open views (the users table, the OU
// rail counts, a details sidebar) refetch without prop-drilling a callback
// across the page↔sidebar boundary — the sidebar is rendered by the layout, not
// the page, so a shared event is the clean seam.
export const accessChanged = createEvent()
