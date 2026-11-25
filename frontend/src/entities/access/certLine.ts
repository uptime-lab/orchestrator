// Parses an OpenSSH authorized_keys / TrustedUserCAKeys "cert-authority" line
// like:
//   from="203.0.113.0/24",principals="root,ops",cert-authority ssh-rsa AAAA... ssh-ca@vault.example.com
// into the pieces our model needs: the bare CA public key, a suggested name
// (the key comment), and the principals the CA is trusted for. The from=
// source restriction is surfaced but not applied (we do not model it).

export type ParsedCertLine = {
  keyContent: string // "ssh-rsa AAAA... comment" — options stripped, ready for the inventory
  comment: string // the trailing comment, if any (used as a name suggestion)
  principals: string[] // from principals="a,b,c"
  from: string[] // from from="cidr,cidr" — informational only
  isCertAuthority: boolean // whether the cert-authority option was present
}

// Matches the key part of a line: a known key-type token, its base64 blob, and
// an optional comment tail. Anchored so the options prefix is whatever precedes.
const KEY_RE =
  /((?:ssh-(?:rsa|ed25519|dss)|ecdsa-sha2-[a-z0-9-]+|sk-[a-z0-9@.-]+)\s+[A-Za-z0-9+/=]+(?:\s+\S.*)?)\s*$/

const optionValues = (options: string, name: string): string[] => {
  const m = new RegExp(`${name}="([^"]*)"`).exec(options)
  if (!m) return []
  return m[1]
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

export const parseCertAuthorityLine = (raw: string): ParsedCertLine | null => {
  const line = raw.trim()
  if (!line) return null
  const m = KEY_RE.exec(line)
  if (!m) return null

  const keyPart = m[1].trim()
  const options = line.slice(0, m.index) // everything before the key = the options field

  // Comment = anything after the blob (third whitespace-separated field on).
  const fields = keyPart.split(/\s+/)
  const comment = fields.length >= 3 ? fields.slice(2).join(' ') : ''

  return {
    keyContent: keyPart,
    comment,
    principals: optionValues(options, 'principals'),
    from: optionValues(options, 'from'),
    isCertAuthority: /(^|,)\s*cert-authority(\s|,|$)/.test(options),
  }
}

// A friendly name from a CA key comment: "ssh-ca@vault.example.com" ->
// "ssh-ca". Falls back to the whole comment, then a generic label.
export const nameFromComment = (comment: string): string => {
  const trimmed = comment.trim()
  if (!trimmed) return 'CA'
  return trimmed.split('@')[0] || trimmed
}
