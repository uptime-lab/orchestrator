import type { NodeCommand } from '@/entities/node/api/types'

// Decisions behind a command-history card, kept apart from the JSX so they
// can be tested without rendering.

// A run_script timeout arrives as a JSON number. Anything else (absent, zero,
// a string) is "no timeout of its own", and the card shows no Timeout row
// rather than an empty "s".
export const scriptTimeoutSeconds = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0
    ? value
    : null

// What the node reported is shown for a command that did not succeed (the
// summary in details says WHICH part failed, the result says why), and for a
// successful run_script, whose output is the whole point of running it.
export const showsReportedOutput = (
  command: Pick<NodeCommand, 'slug' | 'status' | 'result'>,
): boolean => {
  if (!command.result || Object.keys(command.result).length === 0) {
    return false
  }
  return command.status !== 'success' || command.slug === 'run_script'
}

// Output longer than this many lines (or characters) starts collapsed.
const LONG_OUTPUT_LINES = 12
const LONG_OUTPUT_CHARS = 1200

export const isLongOutput = (result: Record<string, string>): boolean => {
  const text = Object.values(result).join('\n')
  return (
    text.length > LONG_OUTPUT_CHARS ||
    text.split('\n').length > LONG_OUTPUT_LINES
  )
}

// After a failed install the server boots the node into recovery until an
// install succeeds; the card that shows the failure says so.
export const failedInstallStaysOnRecovery = (
  command: Pick<NodeCommand, 'slug' | 'status'>,
): boolean => command.slug === 'install_os' && command.status === 'failed'
