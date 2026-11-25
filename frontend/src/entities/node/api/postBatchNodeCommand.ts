import { t } from 'i18next'

import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import { skipReasonText } from '../skipReason.ts'

export type BatchNodeCommandRequest = {
  node_ids: string[] // Array of node IDs to send command to
  command: string // "reboot", "shutdown", "install_os", "install_agent", "provision_connection", "update_boot_order"
  params?: Record<string, unknown>
}

export type NodeCommandResultItem = {
  node_id: string
  success: boolean
  command_id?: string | null
  error?: string | null
  // Queued marks a command accepted for a node that is currently OFFLINE: it
  // waits and is delivered on reconnect. `success` is true and nothing has been
  // applied — the backend DTO says so outright, and a caller that renders this
  // as done is lying about the fleet.
  queued?: boolean
  // Skipped marks a node deliberately not attempted; skip_reason names the
  // precondition it failed (`agent_too_old`, `agent_already_current`).
  // Reporting it as a bare failure sends the operator hunting for a bug when
  // the answer is an update, or nothing at all.
  skipped?: boolean
  skip_reason?: string
}

export type BatchCommandResponse = {
  results: NodeCommandResultItem[]
  total_count: number
  // success_count counts commands ACCEPTED, which includes queued ones.
  success_count: number
  failure_count: number
  queued_count: number
  skipped_count: number
  skipped_by_reason?: Record<string, number>
}

// The endpoint answers 200 for a per-node refusal — it folds each error into a
// result item — so the response BODY, not the HTTP status, is where the outcome
// lives. It also goes through the standard `{success, data, …}` envelope like
// every other handler, which this client used to ignore: it typed
// `response.data` as the result, so every count read `undefined` even for a
// caller that tried to read them.
export const postBatchNodeCommand = async (
  request: BatchNodeCommandRequest,
): Promise<BatchCommandResponse> => {
  const response = await apiClient.post<ApiResponse<BatchCommandResponse>>(
    '/api/v1/nodes/commands',
    request,
  )
  return response.data.data
}

export type BatchOutcome = {
  tone: 'success' | 'warning' | 'error'
  // headline is the one line an operator reads; detail names the nodes that did
  // not get what was asked, which is the part the silent version discarded.
  headline: string
  detail?: string
}

/**
 * summarizeBatchCommand turns a batch response into what to tell the operator.
 *
 * The rule it encodes: a batch is "sent" only when every node accepted it and
 * none of them merely queued. Anything else names the shortfall, because the
 * failure this exists to remove is a dialog that closes silently on a batch
 * where a third of the selection was refused. The single-node path already says
 * "Reboot sent" / "was not accepted", so the silence sat exactly where the blast
 * radius is largest.
 *
 * `nodeLabel` resolves an id to something an operator recognises — ids are
 * useless in a toast — and falls back to the id for a node not in the list.
 */
export const summarizeBatchCommand = (
  action: string,
  result: BatchCommandResponse,
  nodeLabel: (nodeId: string) => string,
): BatchOutcome => {
  const items = result?.results ?? []
  const total = result?.total_count ?? items.length
  // A SKIPPED item is a failure item: the backend builds
  // `{Success: false, Error: …}` and only then flips `Skipped` for an
  // agent-too-old refusal, and its own consumer reads it that way. Classifying
  // it as `success && skipped` made the bucket permanently empty and reported
  // "update these agents" as an indistinguishable wall of refusals — which is
  // the exact outcome both the backend's comment and this helper exist to
  // prevent. Skipped is therefore taken out of `failed` first.
  const skipped = items.filter((r) => r.skipped)
  const failed = items.filter((r) => !r.success && !r.skipped)
  const queued = items.filter((r) => r.success && r.queued)
  const applied = total - failed.length - skipped.length - queued.length

  const names = (list: NodeCommandResultItem[], limit = 4): string => {
    const shown = list.slice(0, limit).map((r) => nodeLabel(r.node_id))
    const rest = list.length - shown.length
    return rest > 0
      ? t('entities.node.batchCommand.namesMore', {
          names: shown.join(', '),
          count: rest,
        })
      : shown.join(', ')
  }

  const parts: string[] = []
  if (queued.length > 0) {
    parts.push(
      t('entities.node.batchCommand.queuedPart', {
        count: queued.length,
        names: names(queued),
      }),
    )
  }
  if (skipped.length > 0) {
    // Read from the items rather than only from the aggregate: the aggregate is
    // the server's own count and could disagree with the list after a partial
    // read, and the item is what the operator is being shown.
    // One distinct reason is named; a mix of reasons is left to the names.
    const reasons = new Set(
      [
        ...Object.keys(result?.skipped_by_reason ?? {}),
        ...skipped.map((r) => r.skip_reason),
      ].filter((r): r is string => !!r),
    )
    const only =
      reasons.size === 1 ? skipReasonText([...reasons][0]) : undefined
    const why = only
      ? t('entities.node.batchCommand.skippedReason', { reason: only })
      : ''
    parts.push(
      t('entities.node.batchCommand.skippedPart', {
        count: skipped.length,
        why,
        names: names(skipped),
      }),
    )
  }
  if (failed.length > 0) {
    // One distinct reason is worth naming; several would turn a toast into a log.
    const reasons = Array.from(
      new Set(failed.map((r) => r.error).filter((e): e is string => !!e)),
    )
    const why =
      reasons.length === 1
        ? t('entities.node.batchCommand.failedReason', { reason: reasons[0] })
        : ''
    parts.push(
      t('entities.node.batchCommand.failedPart', {
        count: failed.length,
        why,
        names: names(failed),
      }),
    )
  }

  if (failed.length > 0) {
    return {
      tone: 'error',
      headline: t('entities.node.batchCommand.headlineAccepted', {
        action,
        applied,
        count: total,
      }),
      detail: parts.join('. '),
    }
  }
  if (queued.length > 0 || skipped.length > 0) {
    return {
      tone: 'warning',
      headline: t('entities.node.batchCommand.headlineAccepted', {
        action,
        applied,
        count: total,
      }),
      detail: parts.join('. '),
    }
  }
  return {
    tone: 'success',
    headline: t('entities.node.batchCommand.headlineSent', {
      action,
      count: total,
    }),
  }
}
