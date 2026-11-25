// This method is needed for typesafe switch-case.
//
// If you call it in `default:`, TS will warn if switch doesn't support all the properties
export const exhaustiveMatchingGuard = (badValue: never) => {
  // eslint-disable-next-line no-console
  console.error(`Should not be here: ${badValue}`)
  return null
}
