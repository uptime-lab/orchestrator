// The contact an operator configures for "write to us for more devices" is
// free text, typically an e-mail address. It is always rendered as text; an
// e-mail address also becomes a mailto link. Nothing else is ever a link:
// the value is shown to every tenant, so it must not be able to point a
// browser anywhere but a mail client.
const EMAIL =
  /^[^\s@<>"'(),;:\\[\]]+@[^\s@<>"'(),;:\\[\]]+\.[^\s@<>"'(),;:\\[\]]+$/

export const isEmailContact = (contact: string): boolean => EMAIL.test(contact)

// contactHref is the mailto link for an e-mail contact, empty otherwise.
export const contactHref = (contact: string): string =>
  isEmailContact(contact) ? `mailto:${contact}` : ''
