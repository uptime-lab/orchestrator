import clsx from 'clsx'
import { useTranslation } from 'react-i18next'

import type { Secret, SecretKind } from '../../api/types'

export type SecretSortKey = 'name' | 'created_at' | 'updated_at'

// dataTransfer key for dragging a secret row onto the OU filter tree.
export const SECRET_DND_TYPE = 'text/secret-id'

export type SecretSort = {
  key: SecretSortKey
  dir: 'asc' | 'desc'
}

export type SecretTableProps = {
  secrets: Secret[]
  // Server-side sort state, controlled by the page.
  sort: SecretSort | null
  onSortChange: (key: SecretSortKey) => void
  selectedSecretId?: string
  onRowClick: (secretId: string) => void
  // Clicking a tag chip adds it to the active tag filter.
  onTagClick?: (tag: string) => void
  // True when the list could not be READ (a 403 for a role without secret.read,
  // or any transient failure) rather than being genuinely empty. The two are
  // indistinguishable from the secrets array alone, and inviting someone to
  // "create your first secret" is wrong in both cases: the Create button's own
  // permission gate has already hidden it, and after a transient failure the
  // message reads as data loss.
  loadFailed?: boolean
}

const kindLabelKey = {
  env: 'entities.secret.kind.env',
  credential: 'entities.secret.kind.credential',
  opaque: 'entities.secret.kind.opaque',
} as const satisfies Record<SecretKind, string>

// created_at/updated_at are RFC3339 strings; last_used_at stays epoch seconds.
const formatDate = (date: string | number | undefined) => {
  if (!date) return '—'
  return new Date(
    typeof date === 'number' ? date * 1000 : date,
  ).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

const LockGlyph = () => (
  <svg
    className="w-3.5 h-3.5 inline-block align-text-bottom"
    viewBox="0 0 20 20"
    fill="currentColor"
  >
    <path
      fillRule="evenodd"
      d="M10 2a4 4 0 00-4 4v2H5a2 2 0 00-2 2v6a2 2 0 002 2h10a2 2 0 002-2v-6a2 2 0 00-2-2h-1V6a4 4 0 00-4-4zm2 6V6a2 2 0 10-4 0v2h4z"
      clipRule="evenodd"
    />
  </svg>
)

// Where a secret applies: direct OU/node attachments, or via one of its tags
// when it has no direct ones of its own.
const AttachedCell = ({ secret }: { secret: Secret }) => {
  const { t } = useTranslation()
  const parts: string[] = []
  if (secret.ou_count > 0)
    parts.push(t('entities.secret.table.ouCount', { count: secret.ou_count }))
  if (secret.node_count > 0)
    parts.push(
      t('entities.secret.table.nodeCount', { count: secret.node_count }),
    )
  if (parts.length > 0) {
    return <span>{parts.join(' · ')}</span>
  }
  if (secret.tags.length > 0) {
    return (
      <span
        className="text-neutral-5 truncate block"
        title={t('entities.secret.table.viaTagTitle')}
      >
        {t('entities.secret.table.viaTag', { tag: secret.tags[0] })}
      </span>
    )
  }
  return (
    <span
      className="text-yellow-6"
      title={t('entities.secret.table.notAttachedTitle')}
    >
      {t('entities.secret.table.notAttached')}
    </span>
  )
}

// Secrets as a table driven by server-side sorting/paging (the page owns the
// query state). Non-sensitive entries ("variables") show their value inline.
export const SecretTable = ({
  secrets,
  sort,
  onSortChange,
  selectedSecretId,
  onRowClick,
  onTagClick,
  loadFailed,
}: SecretTableProps) => {
  const { t } = useTranslation()

  const sortHeader = (key: SecretSortKey, label: string) => (
    <button
      type="button"
      onClick={() => onSortChange(key)}
      className={clsx(
        'flex items-center gap-1 uppercase tracking-wide transition-colors',
        sort?.key === key
          ? 'text-lime-6'
          : 'text-neutral-7 hover:text-neutral-4',
      )}
    >
      {label}
      <span className="w-2">
        {sort?.key === key ? (sort.dir === 'asc' ? '↑' : '↓') : ''}
      </span>
    </button>
  )

  if (secrets.length === 0) {
    return (
      <div className="text-center text-neutral-4 py-12">
        {loadFailed
          ? t('entities.secret.loadFailed')
          : t('entities.secret.empty')}
      </div>
    )
  }

  return (
    <div className="@container flex flex-col gap-2">
      <div
        className={clsx(
          'hidden @[30rem]:flex items-center gap-4 px-4 pb-1',
          'text-xxs uppercase tracking-wide text-neutral-7 select-none',
        )}
      >
        <div className="w-56 flex-shrink-0">
          {sortHeader('name', t('entities.secret.table.name'))}
        </div>
        <div className="flex items-center gap-6 flex-1 min-w-0">
          <div className="w-24 flex-shrink-0">
            {t('entities.secret.table.kind')}
          </div>
          <div className="flex-1 min-w-0 hidden @[40rem]:block">
            {t('entities.secret.table.value')}
          </div>
          <div className="w-36 flex-shrink-0 hidden @[46rem]:block">
            {t('entities.secret.table.attached')}
          </div>
          <div className="w-28 flex-shrink-0 hidden @[64rem]:block">
            {t('entities.secret.table.lastUsed')}
          </div>
          <div className="w-28 flex-shrink-0 hidden @[58rem]:block">
            {sortHeader('updated_at', t('entities.secret.table.updated'))}
          </div>
        </div>
      </div>

      {secrets.map((secret) => (
        <div
          key={secret.id}
          draggable
          onDragStart={(e) => {
            e.dataTransfer.setData(SECRET_DND_TYPE, secret.id)
            e.dataTransfer.effectAllowed = 'move'
          }}
          onClick={() => onRowClick(secret.id)}
          className={clsx(
            'group flex items-center gap-4 rounded px-4 py-3 cursor-pointer transition-colors',
            selectedSecretId === secret.id
              ? 'bg-[#3C4B12]'
              : 'bg-neutral-14 hover:bg-neutral-13',
          )}
        >
          <div className="w-56 flex-shrink-0 min-w-0">
            <div className="font-mono font-bold text-sm text-neutral-1 truncate">
              {secret.name}
            </div>
            {secret.key && (
              <div className="font-mono text-xs text-neutral-5 truncate">
                {secret.key}
              </div>
            )}
            {secret.tags.length > 0 && (
              <div className="flex flex-wrap gap-1 mt-1">
                {secret.tags.map((tag) => (
                  <button
                    key={tag}
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      onTagClick?.(tag)
                    }}
                    className="text-xxs font-mono px-1.5 py-px rounded bg-neutral-12 text-neutral-4 hover:text-lime-6 transition-colors"
                  >
                    #{tag}
                  </button>
                ))}
              </div>
            )}
          </div>
          <div className="flex items-center gap-6 flex-1 min-w-0">
            <div className="w-24 flex-shrink-0">
              <span
                className={clsx(
                  'border rounded-[3px] px-1.5 py-px text-xxs uppercase',
                  secret.sensitive
                    ? 'border-neutral-9 text-neutral-5'
                    : 'border-blue-7 text-blue-5',
                )}
              >
                {secret.sensitive
                  ? t(kindLabelKey[secret.kind])
                  : t('entities.secret.variable')}
              </span>
            </div>
            <div className="flex-1 min-w-0 hidden @[40rem]:block font-mono text-sm">
              {secret.unreadable ? (
                <span
                  className="text-orange-5"
                  title={t('entities.secret.table.unreadableTitle')}
                >
                  {t('entities.secret.table.unreadable')}
                </span>
              ) : secret.sensitive ? (
                <span className="text-neutral-6">
                  {secret.has_value ? (
                    <>
                      <LockGlyph /> ••••••••
                    </>
                  ) : (
                    '—'
                  )}
                </span>
              ) : (
                <span
                  className="text-neutral-2 truncate block"
                  title={secret.value}
                >
                  {secret.value || '—'}
                </span>
              )}
            </div>
            <div className="w-36 flex-shrink-0 hidden @[46rem]:block font-mono text-sm text-neutral-3">
              <AttachedCell secret={secret} />
            </div>
            <div className="w-28 flex-shrink-0 hidden @[64rem]:block font-mono text-sm text-neutral-4">
              {formatDate(secret.last_used_at)}
            </div>
            <div className="w-28 flex-shrink-0 hidden @[58rem]:block font-mono text-sm text-neutral-4">
              {formatDate(secret.updated_at)}
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}
