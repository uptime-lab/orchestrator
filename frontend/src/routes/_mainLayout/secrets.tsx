import { createFileRoute, useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { useUnit } from 'effector-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { OU_FILTER_ALL } from '@/widgets/ouRail'
import { OURailAside } from '@/widgets/ouRail/OURailAside.tsx'
import { SecretTagsTab } from '@/widgets/secretTagsTab'
import { SidebarEnum } from '@/widgets/sidebars/types.ts'

import {
  masterTokensModel,
  TokenList,
  useLoadMasterTokens,
  useMasterTokens,
} from '@/entities/masterToken'
import { TOKEN_DND_TYPE } from '@/entities/masterToken/ui/tokenList/tokenCard'
import { nodesModel } from '@/entities/node'
import { $nodes } from '@/entities/node/model'
import { ousModel, useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import {
  apiSecrets,
  SECRET_DND_TYPE,
  SECRET_TAG_DND_TYPE,
  secretsModel,
  SecretTable,
  useSecretOUCounts,
  useSecrets,
  useSecretTags,
  type SecretKind,
  type SecretListParams,
  type SecretSort,
  type SecretSortKey,
} from '@/entities/secret'

import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import { showError } from '@/shared/ui/toast'

// Colored dot for a tag's palette color in the rail (literal classes so
// Tailwind keeps them; same palette as the Tags tab / backend).
const TAG_DOT: Record<string, string> = {
  red: 'bg-red-7',
  pink: 'bg-pink-7',
  purple: 'bg-purple-5',
  violet: 'bg-violet-5',
  blue: 'bg-blue-8',
  lightBlue: 'bg-lightBlue-8',
  green: 'bg-green-8',
  lime: 'bg-lime-7',
  yellow: 'bg-yellow-5',
  orange: 'bg-orange-5',
  neutral: 'bg-neutral-5',
}

type SecretsTab = 'tokens' | 'secrets' | 'tags'

const isSecretsTab = (value: unknown): value is SecretsTab =>
  value === 'tokens' || value === 'secrets' || value === 'tags'

// Variables are non-sensitive entries; secrets are write-only.
type SecretsView = 'all' | 'variables' | 'secrets'

const isSecretsView = (value: unknown): value is SecretsView =>
  value === 'all' || value === 'variables' || value === 'secrets'

const isSecretKind = (value: unknown): value is SecretKind =>
  value === 'env' || value === 'credential' || value === 'opaque'

// The "Not attached" pseudo-entry of the OU rail.
const OU_FILTER_UNATTACHED = 'unattached'

// A tag filter matcher for the keys/tokens client-side filtering.
const hasAllTags = (itemTags: string[], wanted: string[]) =>
  wanted.every((tag) => itemTags.includes(tag))

const PAGE_SIZE_KEY = 'secrets-page-size'
const PAGE_SIZES = [10, 20, 50, 100]

const readPageSize = () => {
  const stored = Number(localStorage.getItem(PAGE_SIZE_KEY))
  return PAGE_SIZES.includes(stored) ? stored : 50
}

const filterSelectClassName =
  'bg-neutral-13 rounded px-2.5 py-1.5 text-sm text-white outline-none transition-colors disabled:opacity-50'

// Filter state carried in the URL so views are shareable and survive reload.
type SecretsFilterSearch = {
  q?: string
  view?: SecretsView
  kind?: SecretKind
  tags?: string
  ou?: string
  node?: string
}

const parseFilterSearch = (
  search: Record<string, unknown>,
): SecretsFilterSearch => {
  const out: SecretsFilterSearch = {}
  if (typeof search.q === 'string' && search.q) out.q = search.q
  if (isSecretsView(search.view) && search.view !== 'all')
    out.view = search.view
  if (isSecretKind(search.kind)) out.kind = search.kind
  if (typeof search.tags === 'string' && search.tags) out.tags = search.tags
  if (typeof search.ou === 'string' && search.ou) out.ou = search.ou
  if (typeof search.node === 'string' && search.node) out.node = search.node
  return out
}

export const Route = createFileRoute('/_mainLayout/secrets')({
  validateSearch: (search: Record<string, unknown>) => {
    const tab: SecretsTab = isSecretsTab(search.tab) ? search.tab : 'secrets'
    const filters = parseFilterSearch(search)
    const sidebar = search.sidebar
    if (
      (sidebar === 'sshKeyDetails' || sidebar === 'editSSHKey') &&
      typeof search.keyId === 'string'
    ) {
      return { tab, ...filters, sidebar, keyId: search.keyId }
    }
    if (sidebar === 'createSSHKey') {
      return { tab, ...filters, sidebar: 'createSSHKey' as const }
    }
    if (
      sidebar === 'masterTokenDetails' &&
      typeof search.tokenId === 'string'
    ) {
      return {
        tab,
        ...filters,
        sidebar: 'masterTokenDetails' as const,
        tokenId: search.tokenId,
      }
    }
    if (sidebar === 'createMasterToken') {
      return { tab, ...filters, sidebar: 'createMasterToken' as const }
    }
    if (sidebar === 'secretDetails' && typeof search.secretId === 'string') {
      return {
        tab,
        ...filters,
        sidebar: 'secretDetails' as const,
        secretId: search.secretId,
      }
    }
    if (sidebar === 'createSecret') {
      return { tab, ...filters, sidebar: 'createSecret' as const }
    }
    // OU sidebars reachable from the rail's edit mode.
    return { tab, ...filters, sidebar: undefined }
  },
  component: () => <SecretsPage />,
  // allSettled, not all: these endpoints sit behind separate grants, and an
  // operator may hold one without the others. With Promise.all a single 403
  // rejected the whole loader, which (now that the app has an error boundary at
  // all) renders an error card in place of a page the user could partly use —
  // and before the boundary existed, a blank one. Same reasoning and the same
  // shape as the layout loader in _mainLayout.tsx.
  loader: async () => {
    // The token list and the secret facets are fetched by the page once the
    // permission map says the viewer may read them.
    await Promise.allSettled([ousModel.fetchOUsFx()])
  },
})

const SecretsPage = () => {
  // SSH keys and adoption tokens are separate grants: an operator may hold one
  // without the other, so each tab's create button follows its own.
  const check = useCan()
  const canManageTokens = check(Actions.MasterTokenManage)
  useLoadMasterTokens(canManageTokens)
  // Every secrets route needs secret.read. Without it the two tabs are not
  // mounted, so nothing asks for a list the server refuses.
  const canReadSecrets = check(Actions.SecretRead)
  useEffect(() => {
    if (!canReadSecrets) return
    secretsModel.fetchSecretTagsFx().catch(() => {})
    secretsModel.fetchSecretOUCountsFx().catch(() => {})
  }, [canReadSecrets])
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = Route.useSearch()
  const tab = search.tab
  const { tokens } = useMasterTokens()
  const { ous } = useOUs()

  // Client-side OU/tag filtering for the tokens tab (its list is small and
  // fully loaded; secrets filter server-side).
  const railTags = useMemo(
    () => (search.tags ? search.tags.split(',') : []),
    [search.tags],
  )
  const visibleTokenIds = useMemo(() => {
    if (!search.ou && railTags.length === 0) return undefined
    return tokens
      .filter((token) => hasAllTags(token.tags, railTags))
      .filter((token) => {
        if (!search.ou) return true
        if (search.ou === OU_FILTER_UNATTACHED) return !token.ou_id
        return token.ou_id === search.ou
      })
      .map((token) => token.id)
  }, [tokens, ous, search.ou, railTags])

  const setTab = useCallback(
    (next: SecretsTab) => {
      navigate({
        to: '.',
        search: { tab: next, sidebar: undefined },
        replace: true,
      })
    },
    [navigate],
  )

  const handleTokenClick = useCallback(
    (tokenId: string) => {
      navigate({
        to: '.',
        search: {
          tab: 'tokens',
          sidebar: SidebarEnum.MasterTokenDetails,
          tokenId,
        },
      })
    },
    [navigate],
  )
  const handleCreateToken = useCallback(() => {
    navigate({
      to: '.',
      search: { tab: 'tokens', sidebar: SidebarEnum.CreateMasterToken },
    })
  }, [navigate])

  const selectedIds = search as {
    sidebar?: string
    keyId?: string
    tokenId?: string
    secretId?: string
  }
  const selectedTokenId =
    selectedIds.sidebar === 'masterTokenDetails'
      ? selectedIds.tokenId
      : undefined
  const selectedSecretId =
    selectedIds.sidebar === 'secretDetails' ? selectedIds.secretId : undefined

  // Adoption tokens are offered only to a member who may manage them; the
  // list itself needs that right.
  const tabs: { id: SecretsTab; label: string }[] = [
    ...(canManageTokens
      ? [{ id: 'tokens' as const, label: t('pages.secrets.tabs.tokens') }]
      : []),
    { id: 'secrets', label: t('pages.secrets.tabs.secrets') },
    { id: 'tags', label: t('pages.secrets.tabs.tags') },
  ]

  return (
    <div className="h-full w-full flex">
      <SecretsFilterRail />

      <div className="flex-1 min-w-0 overflow-y-auto p-4 pt-8 md:p-8">
        <h1 className="text-lg uppercase font-bold mb-6">
          {t('pages.secrets.title')}
        </h1>

        <div className="flex gap-1 border-b border-neutral-12 mb-6">
          {tabs.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => setTab(item.id)}
              className={clsx(
                'font-mono text-sm px-3 py-2 border-b-2 -mb-px transition-colors',
                tab === item.id
                  ? 'text-lime-6 border-lime-6'
                  : 'text-neutral-4 hover:text-neutral-2 border-transparent',
              )}
            >
              {item.label}
            </button>
          ))}
        </div>

        {tab === 'tokens' && !canManageTokens && (
          <p className="text-sm text-neutral-5">
            {t('pages.secrets.tokensNoPermission')}
          </p>
        )}

        {tab === 'tokens' && canManageTokens && (
          <>
            <div className="flex items-center justify-between gap-4 mb-6">
              <p className="text-sm text-neutral-5 max-w-3xl">
                {t('pages.secrets.hints.tokens')}
                <span className="whitespace-nowrap">
                  &nbsp;
                  <InfoHint text={t('pages.secrets.hints.tokensTooltip')} />
                </span>
              </p>
              <div className="flex items-center gap-2 flex-shrink-0">
                <Button size="md" onClick={handleCreateToken}>
                  {t('pages.secrets.createToken')}
                </Button>
              </div>
            </div>
            <TokenList
              selectedTokenId={selectedTokenId}
              onTokenClick={handleTokenClick}
              visibleIds={visibleTokenIds}
            />
          </>
        )}

        {(tab === 'secrets' || tab === 'tags') && !canReadSecrets && (
          <p className="text-sm text-neutral-5">
            {t('pages.secrets.secretsNoPermission')}
          </p>
        )}

        {tab === 'secrets' && canReadSecrets && (
          <SecretsTabContent selectedSecretId={selectedSecretId} />
        )}

        {tab === 'tags' && canReadSecrets && <SecretTagsTab />}
      </div>
    </div>
  )
}

// Left rail of the Secrets page (all three tabs): OU tree (with edit mode) +
// a Tags section. Selections filter the ACTIVE tab; drops assign the dragged
// entity to the OU.
const SecretsFilterRail = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = Route.useSearch()
  const tab = search.tab
  const { tags: secretTags } = useSecretTags()
  const { ouCounts, fetchOUCounts } = useSecretOUCounts()
  const { ous } = useOUs()
  const { tokens } = useMasterTokens()

  const setFilters = useCallback(
    (partial: Partial<SecretsFilterSearch>) => {
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({ ...prev, ...partial }),
        replace: true,
      })
    },
    [navigate],
  )

  // Per-tab facet sources: the tree and tag list describe the active tab.
  const treeCounts = useMemo(() => {
    const counts: Record<string, number> = {}
    if (tab === 'tokens') {
      for (const ou of ous) {
        counts[ou.id] = tokens.filter((token) => token.ou_id === ou.id).length
      }
    } else {
      for (const entry of ouCounts.ous) {
        counts[entry.ou_id] = entry.count
      }
    }
    return counts
  }, [tab, ouCounts, ous, tokens])

  const allCount = tab === 'tokens' ? tokens.length : ouCounts.total
  const unattachedCount = useMemo(() => {
    if (tab === 'tokens') {
      return tokens.filter((token) => !token.ou_id).length
    }
    return ouCounts.unattached
  }, [tab, tokens, ouCounts])

  const tagEntries = useMemo(() => {
    if (tab === 'secrets' || tab === 'tags') {
      return secretTags.map((entry) => ({
        tag: entry.tag,
        color: entry.color,
        count: entry.count,
        delivered: entry.ou_count + entry.node_count > 0,
      }))
    }
    // For keys/tokens tabs the tag color falls back to the secrets vocabulary
    // (so the same tag reads consistently), else neutral.
    const colorOf = (tag: string) =>
      secretTags.find((e) => e.tag === tag)?.color ?? 'neutral'
    const source = tokens
    const counts = new Map<string, number>()
    for (const item of source) {
      for (const tag of item.tags) counts.set(tag, (counts.get(tag) ?? 0) + 1)
    }
    return [...counts.entries()]
      .map(([tag, count]) => ({
        tag,
        color: colorOf(tag),
        count,
        delivered: false,
      }))
      .sort((a, b) => a.tag.localeCompare(b.tag))
  }, [tab, secretTags, tokens])

  const selectedOU =
    search.ou && search.ou !== OU_FILTER_UNATTACHED
      ? search.ou
      : search.ou
        ? ''
        : OU_FILTER_ALL

  const selectedTags = useMemo(
    () => (search.tags ? search.tags.split(',') : []),
    [search.tags],
  )

  const toggleTag = (tag: string) => {
    const next = selectedTags.includes(tag)
      ? selectedTags.filter((item) => item !== tag)
      : [...selectedTags, tag]
    setFilters({ tags: next.length > 0 ? next.join(',') : undefined })
  }

  // Drop dispatcher: what "assign to OU" means depends on what was dragged.
  const handleDrop = useCallback(
    async (itemId: string, ouId: string | null, type: string) => {
      try {
        switch (type) {
          case SECRET_DND_TYPE:
            if (!ouId) return
            await apiSecrets.attachSecret(itemId, { ou_id: ouId })
            break
          case SECRET_TAG_DND_TYPE:
            if (!ouId) return
            await apiSecrets.attachSecretTag(itemId, { ou_id: ouId })
            break
          case TOKEN_DND_TYPE:
            await masterTokensModel.updateMasterTokenFx({
              tokenId: itemId,
              data: { ou_id: ouId ?? '' },
            })
            break
          default:
            return
        }
        secretsModel.attachmentsChanged()
        fetchOUCounts()
      } catch (err) {
        showError(t('pages.secrets.rail.dropFailed'), err)
      }
    },
    [fetchOUCounts, t],
  )

  const dropTypes =
    tab === 'tokens' ? [TOKEN_DND_TYPE] : [SECRET_DND_TYPE, SECRET_TAG_DND_TYPE]
  const dropHint =
    tab === 'tokens'
      ? t('pages.secrets.rail.dropHintTokens')
      : t('pages.secrets.rail.dropHintSecrets')

  const tagsSection = (
    <div className="flex-none mt-6 pt-4 border-t border-neutral-12">
      <div className="px-3 pb-1 text-xxs uppercase tracking-wide text-neutral-7 select-none">
        {t('pages.secrets.rail.tagsTitle')}
      </div>
      {tagEntries.length === 0 ? (
        <div className="px-3 text-xs text-neutral-6">
          {t('pages.secrets.rail.tagsEmpty')}
        </div>
      ) : (
        tagEntries.map((entry) => {
          const selected = selectedTags.includes(entry.tag)
          return (
            <div key={entry.tag} className="flex items-center">
              <button
                type="button"
                // Draggable onto an OU to attach the tag (delivering every
                // secret that carries it) — on the Tags tab as well as the
                // Secrets tab. The rail has always ACCEPTED text/secret-tag on
                // both, so gating the only source to one of them left the Tags
                // tab advertising a drop nothing could start: a dead capability
                // reads exactly like a broken feature.
                draggable={tab === 'secrets' || tab === 'tags'}
                onDragStart={(e) => {
                  e.dataTransfer.setData(SECRET_TAG_DND_TYPE, entry.tag)
                  e.dataTransfer.effectAllowed = 'move'
                }}
                onClick={() => toggleTag(entry.tag)}
                className={clsx(
                  'flex-1 min-w-0 flex items-center justify-between gap-2 px-3 py-1.5 rounded text-left text-sm font-mono transition-colors',
                  (tab === 'secrets' || tab === 'tags') &&
                    'cursor-grab active:cursor-grabbing',
                  selected
                    ? 'bg-neutral-12 text-lime-6'
                    : 'text-neutral-2 hover:bg-neutral-13',
                )}
                title={
                  entry.delivered
                    ? t('pages.secrets.rail.tagDeliveredTitle')
                    : undefined
                }
              >
                <span className="truncate flex items-center gap-1.5">
                  <span
                    className={clsx(
                      'w-2 h-2 rounded-full flex-shrink-0',
                      TAG_DOT[entry.color] ?? TAG_DOT.neutral,
                    )}
                  />
                  {entry.delivered && <span className="text-lime-6">▸</span>}#
                  {entry.tag}
                </span>
                <span className="text-xs text-neutral-4 flex-shrink-0">
                  {entry.count}
                </span>
              </button>
            </div>
          )
        })
      )}
      {tab === 'secrets' && (
        <div className="px-3 pt-2 text-xs text-neutral-6">
          {t('pages.secrets.rail.tagDropHint')}
        </div>
      )}
    </div>
  )

  const pseudoRows = [
    {
      value: OU_FILTER_ALL,
      label:
        tab === 'tokens'
          ? t('pages.secrets.rail.allTokens')
          : t('pages.secrets.rail.allSecrets'),
      count: allCount,
    },
    {
      value: OU_FILTER_UNATTACHED,
      label: t('pages.secrets.rail.notAttached'),
      count: unattachedCount,
      tone: 'warn' as const,
    },
  ]

  return (
    <>
      <OURailAside
        counts={treeCounts}
        selected={selectedOU}
        onSelect={(value) =>
          setFilters({ ou: value === OU_FILTER_ALL ? undefined : value })
        }
        pseudoRows={pseudoRows}
        dropTypes={dropTypes}
        onDropItem={handleDrop}
        dropHint={dropHint}
        sections={tagsSection}
        // Accept drops without entering OU edit mode, exactly as Credentials
        // does and for the same reason: dragging a secret or a tag onto a unit
        // is the primary way to attach it here, not an edit-mode action.
        // Without this the drag now works and the drop still silently refuses,
        // which looks just as broken one step later.
        alwaysAcceptDrops={dropTypes.length > 0}
      />
    </>
  )
}

// The secrets tab: a server-paginated, filterable table; filters live in the
// URL (managed here and in the rail).
const SecretsTabContent = ({
  selectedSecretId,
}: {
  selectedSecretId?: string
}) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = Route.useSearch()
  const {
    secrets,
    total,
    loading,
    loadFailed: secretsLoadFailed,
    fetchSecrets,
  } = useSecrets()
  const { fetchTags } = useSecretTags()
  const { fetchOUCounts } = useSecretOUCounts()
  const nodes = useUnit($nodes)
  const canManageSecrets = useCan()(Actions.SecretManage)

  const [searchInput, setSearchInput] = useState(search.q ?? '')
  const [sort, setSort] = useState<SecretSort | null>(null)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(readPageSize)

  const view: SecretsView = search.view ?? 'all'
  const kindFilter = search.kind ?? ''
  const tagsFilter = search.tags ?? ''
  const ouFilter = search.ou ?? ''
  const nodeFilter = search.node ?? ''

  const setFilters = useCallback(
    (partial: Partial<SecretsFilterSearch>) => {
      setPage(1)
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({ ...prev, ...partial }),
        replace: true,
      })
    },
    [navigate],
  )

  useEffect(() => {
    const handle = setTimeout(() => {
      if ((search.q ?? '') !== searchInput.trim()) {
        setFilters({ q: searchInput.trim() || undefined })
      }
    }, 300)
    return () => clearTimeout(handle)
  }, [searchInput])

  useEffect(() => {
    if (nodeFilter && nodes.length === 0) {
      nodesModel.fetchNodesFx()
    }
  }, [nodeFilter, nodes.length])
  const nodeFilterName = nodeFilter
    ? (nodes.find((n) => n.id === nodeFilter)?.name ?? nodeFilter.slice(0, 8))
    : ''

  const buildParams = useCallback((): SecretListParams => {
    const params: SecretListParams = { page, per_page: pageSize }
    if (search.q) params.search = search.q
    if (kindFilter) params.kind = kindFilter as SecretKind
    if (view !== 'all') params.sensitive = view === 'secrets'
    if (tagsFilter) params.tags = tagsFilter.split(',')
    if (ouFilter === OU_FILTER_UNATTACHED) {
      params.unattached = true
    } else if (ouFilter) {
      params.ou_id = ouFilter
    }
    if (nodeFilter) params.node_id = nodeFilter
    if (sort) {
      params.sort = sort.key
      params.order = sort.dir
    }
    return params
  }, [
    page,
    pageSize,
    search.q,
    kindFilter,
    view,
    tagsFilter,
    ouFilter,
    nodeFilter,
    sort,
  ])

  useEffect(() => {
    fetchSecrets(buildParams())
  }, [fetchSecrets, buildParams])

  useEffect(() => {
    const refetch = () => {
      fetchSecrets(buildParams())
      fetchTags()
      fetchOUCounts()
    }
    const subs = [
      secretsModel.createSecretFx.done.watch(refetch),
      secretsModel.updateSecretFx.done.watch(refetch),
      secretsModel.deleteSecretFx.done.watch(refetch),
      secretsModel.attachmentsChanged.watch(refetch),
    ]
    return () => subs.forEach((sub) => sub.unsubscribe())
  }, [fetchSecrets, fetchTags, fetchOUCounts, buildParams])

  const handleSortChange = useCallback((key: SecretSortKey) => {
    setSort((prev) => {
      if (prev?.key !== key) return { key, dir: 'asc' }
      if (prev.dir === 'asc') return { key, dir: 'desc' }
      return null
    })
    setPage(1)
  }, [])

  const handleRowClick = useCallback(
    (secretId: string) => {
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          sidebar: SidebarEnum.SecretDetails,
          secretId,
        }),
      })
    },
    [navigate],
  )
  const handleCreateSecret = useCallback(() => {
    navigate({
      to: '.',
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        sidebar: SidebarEnum.CreateSecret,
      }),
    })
  }, [navigate])
  const handleTagClick = useCallback(
    (tag: string) => {
      const current = tagsFilter ? tagsFilter.split(',') : []
      if (!current.includes(tag)) {
        setFilters({ tags: [...current, tag].join(',') })
      }
    },
    [tagsFilter, setFilters],
  )

  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const rangeFrom = total === 0 ? 0 : (page - 1) * pageSize + 1
  const rangeTo = Math.min(page * pageSize, total)

  const views: { id: SecretsView; label: string }[] = [
    { id: 'all', label: t('pages.secrets.views.all') },
    { id: 'variables', label: t('pages.secrets.views.variables') },
    { id: 'secrets', label: t('pages.secrets.views.secrets') },
  ]

  return (
    <>
      <p className="text-sm text-neutral-5 max-w-3xl mb-6">
        {/* The placeholder is passed as an interpolation value so the literal
            {{secrets.<name>}} syntax renders instead of being swallowed by
            i18next's own interpolation. */}
        {t('pages.secrets.hints.secrets', {
          placeholder: '{{secrets.<name>}}',
        })}
        <span className="whitespace-nowrap">
          &nbsp;
          <InfoHint text={t('pages.secrets.hints.secretsTooltip')} />
        </span>
      </p>

      {nodeFilter && (
        <div className="flex items-center gap-2 mb-4">
          <span className="inline-flex items-center gap-2 bg-neutral-12 rounded px-3 py-1.5 text-sm font-mono text-neutral-2">
            {t('pages.secrets.nodeFilter', { node: nodeFilterName })}
            <button
              type="button"
              onClick={() => setFilters({ node: undefined })}
              aria-label={t('pages.secrets.clearNodeFilter')}
              className="text-neutral-6 hover:text-red-6 transition-colors"
            >
              &times;
            </button>
          </span>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3 mb-6">
        <input
          type="search"
          value={searchInput}
          onChange={(e) => setSearchInput(e.target.value)}
          placeholder={t('pages.secrets.searchPlaceholder')}
          className="bg-neutral-13 rounded px-3 py-1.5 text-sm text-white outline-none w-56"
        />

        <div className="flex rounded overflow-hidden border border-neutral-12">
          {views.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() =>
                setFilters({ view: item.id === 'all' ? undefined : item.id })
              }
              className={clsx(
                'font-mono text-xs uppercase px-3 py-1.5 transition-colors',
                view === item.id
                  ? 'bg-neutral-12 text-lime-6'
                  : 'text-neutral-5 hover:text-neutral-2',
              )}
            >
              {item.label}
            </button>
          ))}
        </div>

        <select
          value={kindFilter}
          onChange={(e) =>
            setFilters({
              kind: isSecretKind(e.target.value) ? e.target.value : undefined,
            })
          }
          className={filterSelectClassName}
        >
          <option value="">{t('pages.secrets.filters.allKinds')}</option>
          <option value="env">{t('entities.secret.kind.env')}</option>
          <option value="credential">
            {t('entities.secret.kind.credential')}
          </option>
          <option value="opaque">{t('entities.secret.kind.opaque')}</option>
        </select>

        <div className="flex items-center gap-2 ml-auto">
          {canManageSecrets && (
            <Button size="md" onClick={handleCreateSecret}>
              {t('pages.secrets.createSecret')}
            </Button>
          )}
        </div>
      </div>

      <SecretTable
        loadFailed={secretsLoadFailed}
        secrets={secrets}
        sort={sort}
        onSortChange={handleSortChange}
        selectedSecretId={selectedSecretId}
        onRowClick={handleRowClick}
        onTagClick={handleTagClick}
      />

      <div className="flex items-center justify-between gap-4 mt-4 font-mono text-xs text-neutral-5">
        <div className="flex items-center gap-2">
          <span>{t('pages.secrets.perPage')}</span>
          <select
            value={pageSize}
            onChange={(e) => {
              const next = Number(e.target.value)
              setPageSize(next)
              localStorage.setItem(PAGE_SIZE_KEY, String(next))
              setPage(1)
            }}
            className="bg-neutral-13 rounded px-2 py-1 text-xs text-white outline-none"
          >
            {PAGE_SIZES.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </select>
        </div>
        <div className="flex items-center gap-3">
          <span>
            {loading
              ? t('pages.secrets.loading')
              : t('pages.secrets.range', {
                  from: rangeFrom,
                  to: rangeTo,
                  total,
                })}
          </span>
          <button
            type="button"
            onClick={() => setPage((p) => Math.max(1, p - 1))}
            disabled={page <= 1}
            className="px-2 py-1 text-neutral-4 hover:text-neutral-1 disabled:opacity-40 disabled:cursor-not-allowed"
            aria-label={t('pages.secrets.prevPage')}
          >
            ‹
          </button>
          <button
            type="button"
            onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
            disabled={page >= totalPages}
            className="px-2 py-1 text-neutral-4 hover:text-neutral-1 disabled:opacity-40 disabled:cursor-not-allowed"
            aria-label={t('pages.secrets.nextPage')}
          >
            ›
          </button>
        </div>
      </div>
    </>
  )
}
