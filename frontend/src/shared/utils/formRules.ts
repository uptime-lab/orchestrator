// Input rules the server enforces, checked before a form sends, so the user
// reads which field to fix instead of a refused request.

// isBlank: a name that is empty once trimmed. The server trims too, so a
// name of spaces is no name.
export const isBlank = (value: string): boolean => value.trim() === ''

// A secret is referenced in a playbook as {{secrets.<name>}}, and the
// reference ends at the first "}", so a name carrying one could be stored but
// never referenced. Spaces, dots and other characters are fine.
export const isSecretName = (name: string): boolean =>
  name.trim() !== '' && !name.includes('}')

// isEnvKey: an environment variable name the agent writes on the node —
// letters, digits and '_', not starting with a digit. The agent refuses the
// whole environment set when one key breaks this, which stops every
// provisioning of the node, so the server refuses it too.
const ENV_KEY = /^[A-Za-z_][A-Za-z0-9_]*$/

export const isEnvKey = (key: string): boolean => ENV_KEY.test(key)

// clearsRequiredTitle: an edit that would blank a title the record has. The
// server refuses it; a record stored without a title keeps saving its other
// fields, as the server allows.
export const clearsRequiredTitle = (next: string, stored: string): boolean =>
  isBlank(next) && !isBlank(stored)

// hasLineBreak: an env value is written as one line of an environment file;
// a line break would end the value early and start a line of its own.
export const hasLineBreak = (value: string): boolean => /[\r\n]/.test(value)

// isEmailAddress: one address that can receive mail. A local part and a
// domain with a dot, no spaces and no display-name form; the server parses
// the address fully and stays the authority.
const EMAIL =
  /^[^\s@<>(),;:"[\]\\]+@[^\s@<>(),;:"[\]\\]+\.[^\s@<>(),;:"[\]\\]+$/

export const isEmailAddress = (value: string): boolean =>
  EMAIL.test(value.trim())
