import { Link } from '@tanstack/react-router'
import { FC, useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  apiSecrets,
  secretsModel,
  useSecretTags,
  type EffectiveSecret,
  type Secret,
  type SecretAttachment,
  type SecretKind,
} from '@/entities/secret'

import { extractApiErrorMessage } from '@/shared/utils/apiError'

import {
  directAttachments,
  mayEdit,
  type DirectSecretRow,
  type DirectTagRow,
  type LoadState,
} from './directAttachments'

// Scope the section is attaching secrets to.
type Scope = { kind: 'ou'; ouId: string } | { kind: 'node'; nodeId: string }

export type SecretsAttachmentSectionProps = {
  scope: Scope
  // Whether attach/detach controls are offered. The list itself stays visible:
  // seeing which secrets reach a unit is a read, changing it is not.
  canManage?: boolean
}

// Translation key for a secret kind badge.
const kindLabelKey = {
  env: 'entities.secret.kind.env',
  credential: 'entities.secret.kind.credential',
  opaque: 'entities.secret.kind.opaque',
} as const satisfies Record<SecretKind, string>

export const SecretsAttachmentSection: FC<SecretsAttachmentSectionProps> = ({
  scope,
  canManage = true,
}) => {
  const { t } = useTranslation()
  const { tags: accountTags } = useSecretTags()
  const [effective, setEffective] = useState<EffectiveSecret[]>([])
  const [attachments, setAttachments] = useState<SecretAttachment[]>([])
  const [loadState, setLoadState] = useState<LoadState>('loading')
  const [isProcessing, setIsProcessing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [secretMatches, setSecretMatches] = useState<Secret[]>([])

  const scopeKey = scope.kind === 'ou' ? scope.ouId : scope.nodeId

  // Both reads must succeed before anything can be changed: the scope's own
  // attachment list is what every change is made against, and the effective
  // set is what names them.
  const refresh = useCallback(async () => {
    try {
      const [effectiveResult, attachmentResult] =
        scope.kind === 'ou'
          ? await Promise.all([
              apiSecrets.getOUEffectiveSecrets(scope.ouId),
              apiSecrets.getOUSecretAttachments(scope.ouId),
            ])
          : await Promise.all([
              apiSecrets.getNodeEffectiveSecrets(scope.nodeId),
              apiSecrets.getNodeSecretAttachments(scope.nodeId),
            ])
      setEffective(effectiveResult.secrets)
      setAttachments(attachmentResult.attachments)
      setLoadState('loaded')
    } catch (err) {
      setLoadState('failed')
      setError(
        extractApiErrorMessage(err, t('widgets.secretsAttachment.loadError')),
      )
    }
    // scopeKey captures the identity of the scope for refetching.
  }, [scope.kind, scopeKey, t])

  useEffect(() => {
    setLoadState('loading')
    setError(null)
    secretsModel.fetchSecretTagsFx()
    refresh()
  }, [refresh])

  const editable = canManage && mayEdit(loadState)

  // Whether an effective entry is attached directly at this scope.
  const isDirect = useCallback(
    (entry: EffectiveSecret) =>
      scope.kind === 'node'
        ? entry.source.level === 'node'
        : entry.source.ou_id === scope.ouId,
    [scope.kind, scopeKey],
  )

  const tagCounts = useMemo(
    () => new Map(accountTags.map((entry) => [entry.tag, entry.count])),
    [accountTags],
  )
  const { secrets: directSecrets, tags: directTags } = useMemo(
    () => directAttachments(attachments, effective, isDirect, tagCounts),
    [attachments, effective, isDirect, tagCounts],
  )
  const inherited = useMemo(
    () => effective.filter((e) => !isDirect(e)),
    [effective, isDirect],
  )

  const directSecretIds = useMemo(
    () => directSecrets.map((row) => row.secretId),
    [directSecrets],
  )
  const directTagNames = useMemo(
    () => directTags.map((row) => row.tag),
    [directTags],
  )

  // Server-side secret search for the picker (debounced).
  useEffect(() => {
    if (!query.trim()) {
      setSecretMatches([])
      return
    }
    const handle = setTimeout(async () => {
      try {
        const result = await apiSecrets.getSecrets({
          search: query.trim(),
          per_page: 10,
        })
        setSecretMatches(result.secrets)
      } catch {
        setSecretMatches([])
      }
    }, 300)
    return () => clearTimeout(handle)
  }, [query])

  const tagMatches = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return []
    return accountTags
      .filter(
        (entry) => entry.tag.includes(q) && !directTagNames.includes(entry.tag),
      )
      .slice(0, 5)
  }, [accountTags, query, directTagNames])
  const filteredSecretMatches = useMemo(
    () => secretMatches.filter((s) => !directSecretIds.includes(s.id)),
    [secretMatches, directSecretIds],
  )

  // Each change touches exactly one attachment, then reloads. Nothing is
  // rebuilt from what the panel shows, so an attachment the panel cannot see
  // is never detached by an unrelated edit.
  const mutate = useCallback(
    async (change: () => Promise<unknown>, fallback: string) => {
      setError(null)
      setIsProcessing(true)
      try {
        await change()
      } catch (err) {
        setError(extractApiErrorMessage(err, fallback))
      } finally {
        await refresh()
        setIsProcessing(false)
      }
    },
    [refresh],
  )

  const target = useMemo(
    () =>
      scope.kind === 'ou' ? { ou_id: scope.ouId } : { node_id: scope.nodeId },
    [scope.kind, scopeKey],
  )

  const handleAddSecret = useCallback(
    (secretId: string) => {
      setQuery('')
      mutate(
        () => apiSecrets.attachSecret(secretId, target),
        t('widgets.secretsAttachment.attachError'),
      )
    },
    [mutate, target, t],
  )
  const handleAddTag = useCallback(
    (tag: string) => {
      setQuery('')
      mutate(
        () => apiSecrets.attachSecretTag(tag, target),
        t('widgets.secretsAttachment.attachError'),
      )
    },
    [mutate, target, t],
  )
  const handleRemoveSecret = useCallback(
    (row: DirectSecretRow) => {
      mutate(
        () => apiSecrets.detachSecret(row.secretId, row.attachmentId),
        t('widgets.secretsAttachment.detachError'),
      )
    },
    [mutate, t],
  )
  const handleRemoveTag = useCallback(
    (row: DirectTagRow) => {
      mutate(
        () => apiSecrets.detachSecretTag(row.tag, row.attachmentId),
        t('widgets.secretsAttachment.detachError'),
      )
    },
    [mutate, t],
  )

  const sourceLabel = (entry: EffectiveSecret) => {
    const level =
      entry.source.level === 'node'
        ? t('widgets.secretsAttachment.sourceNode')
        : entry.source.level === 'root'
          ? t('widgets.secretsAttachment.sourceRoot')
          : t('widgets.secretsAttachment.sourceOU')
    return entry.source.via === 'tag' && entry.source.tag
      ? `${level} · ${t('widgets.secretsAttachment.viaTag', {
          tag: entry.source.tag,
        })}`
      : level
  }

  const hasDropdown =
    query.trim() !== '' &&
    (tagMatches.length > 0 || filteredSecretMatches.length > 0)

  return (
    <div className="flex flex-col gap-3 border-t border-neutral-11 pt-4">
      <div className="flex items-center justify-between gap-2">
        <div className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.secretsAttachment.title')}
        </div>
        <Link
          to="/secrets"
          search={
            scope.kind === 'node'
              ? { tab: 'secrets', sidebar: undefined, node: scope.nodeId }
              : { tab: 'secrets', sidebar: undefined, ou: scope.ouId }
          }
          className="text-xs text-neutral-5 hover:text-lime-6 uppercase transition-colors"
        >
          {t('widgets.secretsAttachment.manageLink')} →
        </Link>
      </div>

      {error && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-3 py-2 rounded text-sm">
          {error}
        </div>
      )}

      {loadState === 'loading' ? (
        <div className="text-sm text-neutral-4">
          {t('widgets.secretsAttachment.loading')}
        </div>
      ) : loadState === 'failed' ? (
        <button
          type="button"
          onClick={() => {
            setLoadState('loading')
            setError(null)
            refresh()
          }}
          className="self-start text-xs text-neutral-5 hover:text-lime-6 uppercase transition-colors"
        >
          {t('widgets.secretsAttachment.retry')}
        </button>
      ) : (
        <>
          {directTags.length === 0 && directSecrets.length === 0 && (
            <div className="text-sm text-neutral-4">
              {t('widgets.secretsAttachment.empty')}
            </div>
          )}

          {(directTags.length > 0 || directSecrets.length > 0) && (
            <div className="flex flex-col gap-1">
              {directTags.map((entry) => (
                <div
                  key={entry.attachmentId}
                  className="flex items-center gap-2 bg-neutral-13 rounded px-3 py-2"
                >
                  <span className="text-sm text-neutral-2 truncate flex-1 min-w-0 font-mono">
                    #{entry.tag}
                  </span>
                  <span className="text-xs px-2 py-0.5 rounded bg-neutral-8 text-neutral-2 uppercase flex-shrink-0">
                    {t('widgets.secretsAttachment.tagBadge', {
                      count: entry.memberCount,
                    })}
                  </span>
                  {editable && (
                    <button
                      type="button"
                      onClick={() => handleRemoveTag(entry)}
                      disabled={isProcessing}
                      aria-label={t('widgets.secretsAttachment.detach')}
                      className="flex-shrink-0 text-neutral-7 hover:text-red-6 transition-colors disabled:opacity-50"
                    >
                      &times;
                    </button>
                  )}
                </div>
              ))}
              {directSecrets.map((row) => (
                <div
                  key={row.attachmentId}
                  className="flex items-center gap-2 bg-neutral-13 rounded px-3 py-2"
                >
                  <span className="text-sm text-neutral-2 truncate flex-1 min-w-0">
                    {row.entry?.name ?? row.secretId}
                  </span>
                  {row.entry && (
                    <span className="text-xs px-2 py-0.5 rounded bg-neutral-8 text-neutral-2 uppercase flex-shrink-0">
                      {row.entry.sensitive
                        ? t(kindLabelKey[row.entry.kind])
                        : t('entities.secret.variable')}
                    </span>
                  )}
                  {row.entry?.key && (
                    <span className="text-xs text-neutral-4 font-mono truncate max-w-[30%]">
                      {row.entry.key}
                    </span>
                  )}
                  {editable && (
                    <button
                      type="button"
                      onClick={() => handleRemoveSecret(row)}
                      disabled={isProcessing}
                      aria-label={t('widgets.secretsAttachment.detach')}
                      className="flex-shrink-0 text-neutral-7 hover:text-red-6 transition-colors disabled:opacity-50"
                    >
                      &times;
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}

          {editable && (
            <div className="relative">
              <input
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('widgets.secretsAttachment.searchPlaceholder')}
                disabled={isProcessing}
                className="w-full bg-neutral-13 rounded px-3 py-2 text-sm text-white outline-none disabled:opacity-50"
              />
              {hasDropdown && (
                <div className="absolute z-10 left-0 right-0 mt-1 bg-neutral-12 border border-neutral-11 rounded shadow-lg max-h-64 overflow-y-auto">
                  {tagMatches.map((entry) => (
                    <button
                      key={entry.tag}
                      type="button"
                      onClick={() => handleAddTag(entry.tag)}
                      disabled={isProcessing}
                      className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-neutral-11 transition-colors disabled:opacity-50"
                    >
                      <span className="text-sm text-neutral-2 truncate flex-1 min-w-0 font-mono">
                        #{entry.tag}
                      </span>
                      <span className="text-xs px-2 py-0.5 rounded bg-neutral-8 text-neutral-2 uppercase flex-shrink-0">
                        {t('widgets.secretsAttachment.tagBadge', {
                          count: entry.count,
                        })}
                      </span>
                    </button>
                  ))}
                  {filteredSecretMatches.map((secret) => (
                    <button
                      key={secret.id}
                      type="button"
                      onClick={() => handleAddSecret(secret.id)}
                      disabled={isProcessing}
                      className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-neutral-11 transition-colors disabled:opacity-50"
                    >
                      <span className="text-sm text-neutral-2 truncate flex-1 min-w-0">
                        {secret.name}
                      </span>
                      <span className="text-xs px-2 py-0.5 rounded bg-neutral-8 text-neutral-2 uppercase flex-shrink-0">
                        {secret.sensitive
                          ? t(kindLabelKey[secret.kind])
                          : t('entities.secret.variable')}
                      </span>
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}

          {inherited.length > 0 && (
            <div className="flex flex-col gap-1">
              <div className="text-xs font-medium text-neutral-7 uppercase pt-1">
                {t('widgets.secretsAttachment.inheritedTitle')}
              </div>
              {inherited.map((entry) => (
                <div
                  key={`${entry.id}-${entry.source.level}`}
                  className="flex items-center gap-2 bg-neutral-14 rounded px-3 py-2"
                >
                  <span className="text-sm text-neutral-4 truncate flex-1 min-w-0">
                    {entry.name}
                  </span>
                  <span className="text-xs px-2 py-0.5 rounded bg-neutral-10 text-neutral-4 uppercase flex-shrink-0">
                    {sourceLabel(entry)}
                  </span>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}
