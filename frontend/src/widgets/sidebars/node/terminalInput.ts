// The terminal socket bounds every input frame on the server (64 KiB), because
// each one becomes a single message on the node's gRPC stream and an oversized
// one would end that stream, not just the shell. Keystrokes are bytes; a paste
// is the one input that can be large, and xterm hands it over as ONE onData
// call. So a paste is split here into frames that stay well under the bound.

/** Upper bound, in encoded bytes, of one input frame's `data` string. */
export const MAX_INPUT_FRAME_BYTES = 16 * 1024

// Encoded size of one code point inside a JSON string, in UTF-8 bytes, erring
// on the large side: control characters escape to `\uXXXX`, quote and
// backslash to two bytes, everything else to its UTF-8 length.
function encodedSize(codePoint: number): number {
  if (codePoint < 0x20) return 6
  if (codePoint === 0x22 || codePoint === 0x5c) return 2
  if (codePoint < 0x80) return 1
  if (codePoint < 0x800) return 2
  if (codePoint < 0x10000) return 3
  return 4
}

/**
 * Splits terminal input into chunks whose JSON-encoded `data` value fits in
 * `maxBytes`. Splits on code-point boundaries only, so a surrogate pair is
 * never cut in half. A string that already fits comes back as itself.
 */
export function splitInputFrames(
  data: string,
  maxBytes: number = MAX_INPUT_FRAME_BYTES,
): string[] {
  if (data.length === 0) return []
  const frames: string[] = []
  let start = 0
  let size = 0
  let i = 0
  while (i < data.length) {
    const codePoint = data.codePointAt(i) as number
    const units = codePoint > 0xffff ? 2 : 1
    const bytes = encodedSize(codePoint)
    if (size + bytes > maxBytes && i > start) {
      frames.push(data.slice(start, i))
      start = i
      size = 0
    }
    size += bytes
    i += units
  }
  frames.push(data.slice(start))
  return frames
}
