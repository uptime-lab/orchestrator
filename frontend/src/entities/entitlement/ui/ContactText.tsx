import { Fragment } from 'react'

import { contactHref } from '../contact'

const MARK = '\u0000'

type Props = {
  // The sentence, already translated with the contact interpolated as MARK
  // (see contactSentence), so the whole sentence stays one locale string.
  sentence: string
  contact: string
  className?: string
}

// ContactText renders a sentence that names the contact: the contact as a
// mailto link when it is an e-mail address, as plain text otherwise.
export const ContactText = ({ sentence, contact, className }: Props) => {
  const href = contactHref(contact)
  const parts = sentence.split(MARK)
  return (
    <span>
      {parts.map((part, i) => (
        // The parts are positional: the index is the identity.
        // eslint-disable-next-line react/no-array-index-key
        <Fragment key={i}>
          {part}
          {i < parts.length - 1 &&
            (href ? (
              <a href={href} className={className ?? 'underline'}>
                {contact}
              </a>
            ) : (
              contact
            ))}
        </Fragment>
      ))}
    </span>
  )
}

// CONTACT_MARK is the value to interpolate as {{contact}} for ContactText.
export const CONTACT_MARK = MARK
