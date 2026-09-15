import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { ChevronLeft, Merge, Save, Split, Trash2 } from 'lucide-react'
import { useI18n } from '@/i18n'
import { apiFetch } from '@/lib/api'
import { addAlias, aliasesFor, removeAlias } from '@/lib/alias-merge'
import { Button } from '@/components/ui/button'
import { ConfirmButton } from '@/components/confirm-button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { CopyButton } from '@/components/copy-button'
import { TableSkeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/tooltip'
import { PageHeader } from '@/components/page-header'
import { SortableHeader } from '@/components/sortable-header'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AxisBar, RateLimitBadge } from '@/components/model-table'
import { nextSort, sortRows, type SortState, type SortValueFn } from '@/lib/table-sort'
import {
  fetchCatalog,
  routableRowIds,
  setModelsInGateway,
  type CatalogProvider,
  type KeyStatus,
  type Tier,
} from '@/lib/catalog'
import {
  formatContext,
  groupQuotaBadge,
  isMemberSplit,
  memberEndpointTitle,
  memberOverrideKey,
  memberProviderLabel,
  platformDisplayLabel,
  providerPinId,
  splitsWithoutMember,
  type FallbackEntry,
  type RateLimitUsageData,
  type RoutingData,
  type Row,
} from '@/lib/routing'
import {
  modelSettingsForm,
  reviewModelSettings,
  RANK_MAX,
  RANK_MIN,
  SIZE_LABELS,
  type ModelSettingsPatch,
  type ModelSettingsSource,
} from '@/lib/model-settings'

// The persisted unify overrides (see server model-groups.ts). `splits` forces a
// "platform:model_id" member out of its computed group into its own entry.
type UnifyOverrides = {
  merges: { into: string; keys: string[] }[]
  splits: { member: string; groupKey?: string }[]
}

// What the per-provider split control should do for one member row.
export type SplitAction = {
  kind: 'split' | 'undo'
  pending: boolean
  onClick: () => void
}

const dash = '—'

// USD per 1M tokens. An unknown price is an em dash, never a fabricated zero.
function fmtMoney(v: number | null): string {
  if (v === null) return dash
  if (v < 1) return `$${v.toFixed(2)}`
  return `$${Number.isInteger(v) ? v : v.toFixed(2)}`
}

const PROVIDER_SORT_COLS = ['provider', 'in', 'out', 'context', 'latency', 'throughput', 'reliability', 'requests'] as const
type ProviderSortCol = (typeof PROVIDER_SORT_COLS)[number]

const providerSortValue: SortValueFn<CatalogProvider, ProviderSortCol> = (p, col) => {
  switch (col) {
    case 'provider': return p.label
    case 'in': return p.priceIn
    case 'out': return p.priceOut
    case 'context': return p.contextWindow
    case 'latency': return p.latencyMs
    case 'throughput': return p.throughput
    case 'reliability': return p.reliability
    case 'requests': return p.requests
  }
}

function readProviderSort(params: URLSearchParams): SortState<ProviderSortCol> {
  const column = params.get('sort')
  const direction = params.get('dir')
  if ((PROVIDER_SORT_COLS as readonly string[]).includes(column ?? '') && (direction === 'asc' || direction === 'desc')) {
    return { column: column as ProviderSortCol, direction }
  }
  return null
}

// One model's own page: the id a caller sends, how it is priced and scored, and
// every provider that can serve it — read from the same catalogue the browse
// list uses, so the two can never disagree. Reached from the Models list.
export default function ModelDetailPage() {
  const { t } = useI18n()
  const { id } = useParams<{ id: string }>()
  const canonicalId = id ? decodeURIComponent(id) : ''
  const queryClient = useQueryClient()
  const [params, setParams] = useSearchParams()

  const { data: catalog, isLoading } = useQuery({
    queryKey: ['catalog', 'chat'],
    queryFn: () => fetchCatalog('chat'),
  })
  const model = useMemo(
    () => catalog?.models.find(m => m.canonicalId === canonicalId),
    [catalog, canonicalId],
  )

  // Fallback data still drives the editable settings below and supplies each
  // provider's payment tier, which the catalogue contract does not carry.
  const { data: entries = [] } = useQuery<FallbackEntry[]>({
    queryKey: ['fallback'],
    queryFn: () => apiFetch('/api/fallback'),
  })
  const { data: routing } = useQuery<RoutingData>({
    queryKey: ['fallback', 'routing'],
    queryFn: () => apiFetch('/api/fallback/routing'),
  })
  const { data: keyData } = useQuery<{ apiKey: string }>({
    queryKey: ['unified-key'],
    queryFn: () => apiFetch('/api/settings/api-key'),
  })
  const { data: unify } = useQuery<{ enabled: boolean; overrides: UnifyOverrides }>({
    queryKey: ['unify'],
    queryFn: () => apiFetch('/api/settings/unify'),
  })
  const { data: rateLimitUsage } = useQuery<RateLimitUsageData>({
    queryKey: ['fallback', 'rate-limit-usage'],
    queryFn: () => apiFetch('/api/fallback/rate-limit-usage'),
    refetchInterval: 15_000,
  })
  const rateUsageByModel = useMemo(
    () => new Map((rateLimitUsage?.rows ?? []).map(r => [r.modelDbId, r])),
    [rateLimitUsage],
  )

  // The routing list is written through the catalogue contract so this page,
  // the browse list and the bulk bar can never diverge on how a model goes on.
  const gatewayMutation = useMutation({
    mutationFn: ({ ids, use }: { ids: number[]; use: boolean }) => setModelsInGateway(ids, use),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['catalog'] })
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
    },
  })
  const modelPatchMutation = useMutation({
    mutationFn: ({ modelDbId, patch }: { modelDbId: number; patch: ModelSettingsPatch }) =>
      apiFetch(`/api/models/${modelDbId}`, { method: 'PATCH', body: JSON.stringify(patch) }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      queryClient.invalidateQueries({ queryKey: ['fallback', 'routing'] })
      queryClient.invalidateQueries({ queryKey: ['catalog'] })
      queryClient.invalidateQueries({ queryKey: ['models'] })
    },
  })
  const modelDeleteMutation = useMutation({
    mutationFn: (modelDbId: number) => apiFetch(`/api/models/${modelDbId}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      queryClient.invalidateQueries({ queryKey: ['fallback', 'routing'] })
      queryClient.invalidateQueries({ queryKey: ['catalog'] })
      queryClient.invalidateQueries({ queryKey: ['models'] })
    },
  })

  const [aliasInput, setAliasInput] = useState('')
  // Split a provider's copy out of its unified model (or merge it back). The
  // unify PUT replaces the whole overrides object, so the untouched half of it
  // is sent back verbatim.
  const splitMutation = useMutation({
    mutationFn: (splits: UnifyOverrides['splits']) =>
      apiFetch('/api/settings/unify', {
        method: 'PUT',
        body: JSON.stringify({ overrides: { merges: unify?.overrides.merges ?? [], splits } }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['unify'] })
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      queryClient.invalidateQueries({ queryKey: ['fallback', 'routing'] })
      queryClient.invalidateQueries({ queryKey: ['catalog'] })
      queryClient.invalidateQueries({ queryKey: ['models'] })
    },
  })
  const mergeMutation = useMutation({
    mutationFn: (merges: UnifyOverrides['merges']) =>
      apiFetch('/api/settings/unify', {
        method: 'PUT',
        body: JSON.stringify({ overrides: { merges, splits: unify?.overrides.splits ?? [] } }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['unify'] })
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      queryClient.invalidateQueries({ queryKey: ['fallback', 'routing'] })
      queryClient.invalidateQueries({ queryKey: ['catalog'] })
      queryClient.invalidateQueries({ queryKey: ['models'] })
      setAliasInput('')
    },
  })

  const splits = unify?.overrides.splits ?? []
  function splitActionFor(m: Row, memberCount: number): SplitAction | undefined {
    if (!unify) return undefined
    if (isMemberSplit(splits, m)) {
      return { kind: 'undo', pending: splitMutation.isPending, onClick: () => splitMutation.mutate(splitsWithoutMember(splits, m)) }
    }
    if (memberCount < 2) return undefined
    return { kind: 'split', pending: splitMutation.isPending, onClick: () => splitMutation.mutate([...splits, { member: memberOverrideKey(m) }]) }
  }

  const isManual = (routing?.strategy ?? 'balanced') === 'priority'
  const scoreById = new Map((routing?.scores ?? []).map(s => [s.modelDbId, s]))
  // The configured, keyed rows this model unifies — the ones with editable
  // settings below. The full serving set (keyed or not) comes from the catalogue.
  const members: Row[] = entries
    .filter(e => e.keyCount > 0 && (e.canonicalId ?? e.modelId) === canonicalId)
    .map(e => ({ ...(scoreById.get(e.modelDbId) ?? {}), ...e }))
    .sort((a, b) => (isManual ? a.priority - b.priority : (b.score ?? 0) - (a.score ?? 0)))
  const siblings: Row[] = entries as Row[]

  // Each provider's payment nature: its configured row's own tier, or — when it
  // has no configured row — inferred from whether it publishes a price.
  const tierByDbId = useMemo(() => {
    const map = new Map<number, Tier>()
    for (const e of entries) if (e.tier) map.set(e.modelDbId, e.tier)
    return map
  }, [entries])
  function paymentOf(p: CatalogProvider): Tier {
    const known = tierByDbId.get(p.modelDbId)
    if (known) return known
    return (p.priceIn ?? 0) > 0 || (p.priceOut ?? 0) > 0 ? 'paid' : 'free'
  }

  const sort = readProviderSort(params)
  function toggleSort(col: ProviderSortCol) {
    const next = nextSort(sort, col)
    setParams(prev => {
      const merged = new URLSearchParams(prev)
      if (next) {
        merged.set('sort', next.column)
        merged.set('dir', next.direction)
      } else {
        merged.delete('sort')
        merged.delete('dir')
      }
      return merged
    }, { replace: true })
  }
  const providers = model ? sortRows(model.providers, sort, providerSortValue) : []

  const groupLabel = members[0]?.groupLabel ?? members[0]?.displayName ?? model?.name ?? canonicalId
  const quota = members.length ? groupQuotaBadge(members, t) : null
  const rateRows = members.flatMap(m => rateUsageByModel.get(m.modelDbId) ?? [])
  const merges = useMemo(() => unify?.overrides.merges ?? [], [unify])
  const groupAliases = useMemo(() => aliasesFor(merges, groupLabel), [merges, groupLabel])
  const submitAlias = () => {
    if (!aliasInput.trim()) return
    mergeMutation.mutate(addAlias(merges, groupLabel, aliasInput))
  }

  const baseUrl = import.meta.env.DEV
    ? `http://${window.location.hostname}:${__SERVER_PORT__}/v1`
    : `${window.location.origin}/v1`
  const snippet = `curl ${baseUrl}/chat/completions \\
  -H "Authorization: Bearer ${keyData?.apiKey || 'YOUR_API_KEY'}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${canonicalId}",
    "messages": [
      { "role": "user", "content": "Hello!" }
    ]
  }'`

  return (
    <div>
      <PageHeader
        title={model ? `${platformDisplayLabel(model.author)}: ${model.name}` : canonicalId}
        divider={false}
      />

      <div className="space-y-6">
        <Link to="/models" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ChevronLeft className="size-4" />{t('models.backToModels')}
        </Link>

        {isLoading ? (
          <TableSkeleton rows={4} />
        ) : !model ? (
          <div className="rounded-xl border border-dashed p-8 text-center">
            <p className="text-sm text-muted-foreground">{t('catalog.detail_notFound')}</p>
            <code className="mt-2 inline-block max-w-full truncate rounded-full bg-muted px-2 py-0.5 font-mono text-[11px] text-foreground">{canonicalId}</code>
            <div className="mt-4">
              <Link
                to={`/models?q=${encodeURIComponent(canonicalId)}`}
                className="inline-flex items-center rounded-lg border px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {t('routing.openCatalog')}
              </Link>
            </div>
          </div>
        ) : (
          <>
            {/* Action bar: the id a caller sends, how the model is paid for and
                what it can do, plus the gateway Use switch. */}
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <span className="inline-flex min-w-0 items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-[11px] text-foreground">
                  <code className="min-w-0 truncate font-mono">{model.canonicalId}</code>
                  <CopyButton text={model.canonicalId} label={t('models.copyModelId')} className="size-4 shrink-0 border-0 bg-transparent" />
                </span>
                <CostChip tier={model.tier} provider={platformDisplayLabel(model.author)} />
                {model.caps.tools && <CapChip kind="tools" />}
                {model.caps.vision && <CapChip kind="vision" />}
                {model.caps.reasoning && <CapChip kind="reasoning" />}
              </div>
              <div className="flex shrink-0 items-center gap-3">
                <Link
                  to="/playground"
                  className="inline-flex items-center rounded-lg border px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {t('nav.playground')}
                </Link>
                <label className="inline-flex items-center gap-2 text-sm">
                  <span className="text-muted-foreground">{model.inGateway ? t('catalog.inUse') : t('catalog.use')}</span>
                  <Switch
                    checked={model.inGateway}
                    disabled={gatewayMutation.isPending || routableRowIds(model).length === 0}
                    onCheckedChange={c => gatewayMutation.mutate({ ids: routableRowIds(model), use: c })}
                    aria-label={t('catalog.use')}
                  />
                </label>
              </div>
            </div>

            {/* Stat strip: gap-px over a bordered bg-border box draws the cell
                dividers without a border on every cell. */}
            <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-4">
              <StatCell label={t('catalog.detail_price')}>
                {model.priceIn === null && model.priceOut === null ? dash : `${fmtMoney(model.priceIn)} / ${fmtMoney(model.priceOut)}`}
              </StatCell>
              <StatCell label={t('catalog.detail_context')}>
                {model.contextWindow === null ? dash : formatContext(model.contextWindow)}
              </StatCell>
              <StatCell label={t('catalog.detail_tier')}>
                <CostChip tier={model.tier} provider={platformDisplayLabel(model.author)} />
              </StatCell>
              <StatCell label={t('catalog.detail_score')}>
                {model.score === null ? dash : model.score.toFixed(3)}
              </StatCell>
            </div>

            <div className="rounded-xl border">
              <div className="border-b px-4 py-3">
                <h2 className="text-sm font-medium">{t('catalog.detail_providers')}</h2>
                <p className="mt-0.5 text-xs text-muted-foreground">{t('catalog.detail_providersBlurb')}</p>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <SortableHeader column="provider" label={t('catalog.col_provider')} sort={sort} onToggle={toggleSort} />
                    <SortableHeader column="in" label={t('catalog.col_in')} align="right" sort={sort} onToggle={toggleSort} />
                    <SortableHeader column="out" label={t('catalog.col_out')} align="right" sort={sort} onToggle={toggleSort} />
                    <SortableHeader column="context" label={t('catalog.col_context')} align="right" sort={sort} onToggle={toggleSort} />
                    <SortableHeader column="latency" label={t('catalog.col_latency')} align="right" sort={sort} onToggle={toggleSort} />
                    <SortableHeader column="throughput" label={t('catalog.col_throughput')} align="right" sort={sort} onToggle={toggleSort} />
                    <SortableHeader column="reliability" label={t('catalog.col_reliability')} sort={sort} onToggle={toggleSort} />
                    <SortableHeader column="requests" label={t('catalog.col_requests')} align="right" sort={sort} onToggle={toggleSort} />
                    <TableHead>{t('catalog.col_key')}</TableHead>
                    <TableHead className="text-right">{t('catalog.col_use')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {providers.map(p => (
                    <TableRow key={p.modelDbId}>
                      <TableCell>
                        <div className="flex min-w-0 items-center gap-2">
                          <span className="min-w-0 truncate font-medium">{p.label}</span>
                          <CostChip tier={paymentOf(p)} provider={p.label} />
                        </div>
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{fmtMoney(p.priceIn)}</TableCell>
                      <TableCell className="text-right tabular-nums">{fmtMoney(p.priceOut)}</TableCell>
                      <TableCell className="text-right tabular-nums">{p.contextWindow === null ? dash : formatContext(p.contextWindow)}</TableCell>
                      <TableCell className="text-right tabular-nums">{p.latencyMs === null ? dash : `${Math.round(p.latencyMs)} ms`}</TableCell>
                      <TableCell className="text-right tabular-nums">{p.throughput === null ? dash : `${p.throughput.toFixed(1)} tok/s`}</TableCell>
                      <TableCell>
                        {p.reliability === null
                          ? <span className="text-muted-foreground">{dash}</span>
                          : <AxisBar value={p.reliability / 100} color="#22c55e" />}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{p.requests === null ? dash : p.requests}</TableCell>
                      <TableCell><KeyChip status={p.keyStatus} /></TableCell>
                      <TableCell className="text-right">
                        <Switch
                          checked={p.inGateway}
                          disabled={gatewayMutation.isPending}
                          onCheckedChange={c => gatewayMutation.mutate({ ids: [p.modelDbId], use: c })}
                          aria-label={t('catalog.col_use')}
                        />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            {(quota || rateRows.length > 0) && (
              <div className="flex flex-wrap items-center gap-2">
                {quota && <span title={quota.title} className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground tabular-nums">{quota.text}</span>}
                <RateLimitBadge size="md" rows={rateRows} />
              </div>
            )}

            {members.length > 0 && (
              <>
                <div className="rounded-xl border bg-card p-4">
                  <div className="mb-3">
                    <h2 className="text-sm font-medium">{t('models.settingsHeading')}</h2>
                    <p className="mt-0.5 text-xs text-muted-foreground">{t('models.settingsHint')}</p>
                  </div>
                  <div className="space-y-3">
                    {members.map(m => (
                      <ProviderSettingsRow
                        key={m.modelDbId}
                        model={m}
                        endpointLabel={memberProviderLabel(m, siblings)}
                        endpointTitle={memberEndpointTitle(m, siblings)}
                        saving={modelPatchMutation.isPending && modelPatchMutation.variables?.modelDbId === m.modelDbId}
                        deleting={modelDeleteMutation.isPending && modelDeleteMutation.variables === m.modelDbId}
                        onSave={(patch) => modelPatchMutation.mutate({ modelDbId: m.modelDbId, patch })}
                        onDelete={() => modelDeleteMutation.mutate(m.modelDbId)}
                        splitAction={splitActionFor(m, members.length)}
                      />
                    ))}
                  </div>
                </div>

                <div className="rounded-xl border bg-card p-4">
                  <h2 className="text-sm font-medium">{t('models.providerIdsHeading')}</h2>
                  <p className="mt-0.5 mb-3 text-xs text-muted-foreground">{t('models.providerIdsHint')}</p>
                  <div className="space-y-1.5">
                    {members.map(m => (
                      <div key={m.modelDbId} className="flex items-center gap-2 text-xs">
                        <span className="w-28 max-w-[16rem] shrink-0 basis-auto truncate text-muted-foreground sm:w-auto sm:min-w-28" title={memberEndpointTitle(m, siblings)}>
                          {memberProviderLabel(m, siblings)}
                        </span>
                        <code className="min-w-0 flex-1 truncate font-mono text-[11px]">{providerPinId(m, siblings)}</code>
                        <Tooltip text={t('models.copyModelName')}>
                          <CopyButton text={providerPinId(m, siblings)} label={t('models.copyModelName')} className="border-0 bg-transparent" />
                        </Tooltip>
                      </div>
                    ))}
                  </div>
                </div>

                <div className="rounded-xl border bg-card p-4">
                  <h2 className="text-sm font-medium">{t('models.aliasMergeHeading')}</h2>
                  <p className="mt-0.5 mb-3 text-xs text-muted-foreground">{t('models.aliasMergeHint')}</p>
                  <div className="flex items-center gap-2">
                    <Input
                      value={aliasInput}
                      onChange={e => setAliasInput(e.target.value)}
                      onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); submitAlias() } }}
                      placeholder="custom:my-alias"
                      className="font-mono text-xs"
                      aria-label={t('models.aliasMergeHeading')}
                    />
                    <Button type="button" size="sm" disabled={mergeMutation.isPending || !aliasInput.trim()} onClick={submitAlias}>
                      {t('models.aliasMergeAdd')}
                    </Button>
                  </div>
                  {groupAliases.length > 0 && (
                    <div className="mt-3 space-y-1.5">
                      {groupAliases.map(alias => (
                        <div key={alias} className="flex items-center gap-2 text-xs">
                          <code className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground">{alias}</code>
                          <Button type="button" size="xs" variant="ghost" className="text-muted-foreground" disabled={mergeMutation.isPending} onClick={() => mergeMutation.mutate(removeAlias(merges, groupLabel, alias))}>
                            {t('models.aliasMergeRemove')}
                          </Button>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </>
            )}

            <div className="overflow-hidden rounded-xl border bg-card">
              <div className="flex items-center gap-2 border-b px-3 py-2">
                <CopyButton text={snippet} className="size-7 shrink-0" label={t('common.copy')} />
                <span className="text-xs font-medium">{t('models.codeSnippetHeading')}</span>
              </div>
              <pre className="overflow-x-auto px-4 py-3 text-[11px] leading-relaxed"><code className="font-mono">{snippet}</code></pre>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function ProviderSettingsRow({
  model,
  endpointLabel,
  endpointTitle,
  saving,
  deleting,
  onSave,
  onDelete,
  splitAction,
}: {
  model: Row
  // Which provider this card is for. Carries the endpoint too, but only when
  // two custom endpoints serve the same model id (#651).
  endpointLabel: string
  // Hover text for that label, set only when there was a collision to resolve.
  endpointTitle?: string
  saving: boolean
  deleting: boolean
  onSave: (patch: ModelSettingsPatch) => void
  onDelete: () => void
  splitAction?: SplitAction
}) {
  const { t } = useI18n()
  // `members` is rebuilt on every render, so depend on the model's own values
  // rather than the object identity — otherwise the form resets mid-edit.
  const {
    modelDbId, displayName, contextWindow, intelligenceRank, speedRank, sizeLabel,
    rpmLimit, rpdLimit, tpmLimit, tpdLimit, supportsVision, supportsTools, enabled,
  } = model
  const source: ModelSettingsSource = useMemo(() => ({
    displayName, contextWindow, intelligenceRank, speedRank, sizeLabel,
    rpmLimit, rpdLimit, tpmLimit, tpdLimit, supportsVision, supportsTools, enabled,
  }), [displayName, contextWindow, intelligenceRank, speedRank, sizeLabel, rpmLimit, rpdLimit,
    tpmLimit, tpdLimit, supportsVision, supportsTools, enabled])

  const [form, setForm] = useState(() => modelSettingsForm(source))
  useEffect(() => setForm(modelSettingsForm(source)), [modelDbId, source])
  const setField = <K extends keyof typeof form>(key: K, value: typeof form[K]) =>
    setForm(current => ({ ...current, [key]: value }))

  const { patch, invalid, dirty } = reviewModelSettings(source, form)
  const canSave = dirty && patch !== null && !saving && !deleting
  const sourceLabel = model.source === 'custom' ? t('models.customModel') : t('models.catalogModel')
  // Fields whose effective value comes from a local override instead of the
  // catalog. Custom models are never catalog-managed, so this stays empty.
  const overridden = new Set(model.overrideFields ?? [])

  function save() {
    if (!canSave || !patch) return
    onSave(patch)
  }

  return (
    <div className="rounded-xl border bg-background/60 p-3">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium" title={endpointTitle}>{endpointLabel}</span>
        <code className="min-w-0 truncate rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] text-foreground">{model.modelId}</code>
        <CopyButton text={model.modelId} label={t('models.copyModelId')} className="size-6 shrink-0" />
        <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">{sourceLabel}</span>
        {model.hasOverrides && (
          <span className="rounded-full bg-emerald-600/15 px-1.5 py-0.5 text-[10px] text-emerald-700 dark:text-emerald-400">
            {t('models.localOverride')}
          </span>
        )}
        {splitAction?.kind === 'undo' && (
          <span className="rounded-full bg-amber-600/15 px-1.5 py-0.5 text-[10px] text-amber-700 dark:text-amber-400">
            {t('models.splitBadge')}
          </span>
        )}
        {splitAction && (
          <Tooltip text={t(splitAction.kind === 'split' ? 'models.splitOutHint' : 'models.splitUndoHint')}>
            <Button
              type="button"
              size="xs"
              variant="ghost"
              className="ml-auto text-muted-foreground"
              disabled={splitAction.pending}
              onClick={splitAction.onClick}
            >
              {splitAction.kind === 'split' ? <Split className="size-3" /> : <Merge className="size-3" />}
              {t(splitAction.kind === 'split' ? 'models.splitOut' : 'models.splitUndo')}
            </Button>
          </Tooltip>
        )}
      </div>
      <div className="grid gap-3 md:grid-cols-[minmax(12rem,1fr)_8rem_auto_auto_auto_auto] md:items-end">
        <label className="space-y-1 text-xs text-muted-foreground">
          <FieldLabel text={t('models.displayName')} overridden={overridden.has('displayName')} />
          <Input
            value={form.displayName}
            onChange={e => setField('displayName', e.target.value)}
            aria-invalid={invalid.displayName}
            className="text-sm"
          />
        </label>
        <NumberField
          label={t('models.contextWindow')}
          value={form.contextWindow}
          invalid={invalid.contextWindow}
          overridden={overridden.has('contextWindow')}
          onChange={value => setField('contextWindow', value)}
        />
        <label className="flex h-8 items-center gap-2 text-xs">
          <Switch size="sm" checked={form.supportsTools} onCheckedChange={value => setField('supportsTools', value)} />
          <FieldLabel text={t('models.tools')} overridden={overridden.has('supportsTools')} />
        </label>
        <label className="flex h-8 items-center gap-2 text-xs">
          <Switch size="sm" checked={form.supportsVision} onCheckedChange={value => setField('supportsVision', value)} />
          <FieldLabel text={t('models.vision')} overridden={overridden.has('supportsVision')} />
        </label>
        <label className="flex h-8 items-center gap-2 text-xs">
          <Switch size="sm" checked={form.fallbackEnabled} onCheckedChange={value => setField('fallbackEnabled', value)} />
          <span>{t('models.inFallback')}</span>
        </label>
        <div className="flex items-center justify-end gap-1">
          <Tooltip text={t('models.saveModelSettings')}>
            <Button type="button" size="icon-sm" variant="ghost" disabled={!canSave} onClick={save}>
              <Save className="size-3.5" />
            </Button>
          </Tooltip>
          <ConfirmButton
            variant="destructive"
            size="icon-sm"
            armedSize="xs"
            armedClassName=""
            disabled={saving || deleting}
            onConfirm={onDelete}
            aria-label={t('common.delete')}
          >
            <Trash2 className="size-3.5" />
          </ConfirmButton>
        </div>
      </div>

      {/* Router inputs: the two ranks feed the scoring axes, the four limits
          feed rate-limit accounting. Editable because the catalog can be wrong
          for a given account, or silent about a brand-new model (#551). The
          capability tier sets the intelligence axis — a custom model without
          a recognized tier sits at the floor of it, so it gets a picker here
          (#685). */}
      <div className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-7">
        {/* Full-width on the 2/3-column layouts so the six number fields
            below still tile evenly; one row of seven on desktop. */}
        <label className="col-span-2 space-y-1 text-xs text-muted-foreground sm:col-span-3 md:col-span-1" title={t('models.sizeLabelHint')}>
          <FieldLabel text={t('models.sizeLabel')} overridden={overridden.has('sizeLabel')} />
          <Select value={form.sizeLabel} onValueChange={value => setField('sizeLabel', value ?? '')}>
            <SelectTrigger className="w-full" aria-label={t('models.sizeLabel')}>
              <SelectValue placeholder={t('models.sizeLabelNone')} />
            </SelectTrigger>
            <SelectContent>
              {SIZE_LABELS.map(label => (
                <SelectItem key={label} value={label}>{label}</SelectItem>
              ))}
              <SelectItem value="">{t('models.sizeLabelNone')}</SelectItem>
            </SelectContent>
          </Select>
        </label>
        <NumberField
          label={t('models.intelligenceRank')}
          hint={t('models.rankHint')}
          min={RANK_MIN}
          max={RANK_MAX}
          value={form.intelligenceRank}
          invalid={invalid.intelligenceRank}
          overridden={overridden.has('intelligenceRank')}
          onChange={value => setField('intelligenceRank', value)}
        />
        <NumberField
          label={t('models.speedRank')}
          hint={t('models.rankHint')}
          min={RANK_MIN}
          max={RANK_MAX}
          value={form.speedRank}
          invalid={invalid.speedRank}
          overridden={overridden.has('speedRank')}
          onChange={value => setField('speedRank', value)}
        />
        <NumberField
          label={t('models.limitRpm')}
          hint={t('models.limitRpmHint')}
          value={form.rpmLimit}
          invalid={invalid.rpmLimit}
          overridden={overridden.has('rpmLimit')}
          onChange={value => setField('rpmLimit', value)}
        />
        <NumberField
          label={t('models.limitRpd')}
          hint={t('models.limitRpdHint')}
          value={form.rpdLimit}
          invalid={invalid.rpdLimit}
          overridden={overridden.has('rpdLimit')}
          onChange={value => setField('rpdLimit', value)}
        />
        <NumberField
          label={t('models.limitTpm')}
          hint={t('models.limitTpmHint')}
          value={form.tpmLimit}
          invalid={invalid.tpmLimit}
          overridden={overridden.has('tpmLimit')}
          onChange={value => setField('tpmLimit', value)}
        />
        <NumberField
          label={t('models.limitTpd')}
          hint={t('models.limitTpdHint')}
          value={form.tpdLimit}
          invalid={invalid.tpdLimit}
          overridden={overridden.has('tpdLimit')}
          onChange={value => setField('tpdLimit', value)}
        />
      </div>
    </div>
  )
}

// A field label that says whether the value below it is still the catalog's or
// has been replaced locally — the same emerald the row-level badge uses.
function FieldLabel({ text, overridden }: { text: string; overridden: boolean }) {
  const { t } = useI18n()
  return (
    <span className="flex items-center gap-1">
      {text}
      {overridden && (
        <span
          title={t('models.localOverride')}
          aria-label={t('models.localOverride')}
          className="size-1.5 shrink-0 rounded-full bg-emerald-600 dark:bg-emerald-400"
        />
      )}
    </span>
  )
}

function NumberField({
  label,
  hint,
  value,
  invalid,
  overridden,
  onChange,
  min = 1,
  max,
}: {
  label: string
  hint?: string
  value: string
  invalid: boolean
  overridden: boolean
  onChange: (value: string) => void
  min?: number
  max?: number
}) {
  return (
    <label className="space-y-1 text-xs text-muted-foreground" title={hint}>
      <FieldLabel text={label} overridden={overridden} />
      <Input
        type="number"
        min={min}
        max={max}
        step={1}
        value={value}
        onChange={e => onChange(e.target.value)}
        aria-invalid={invalid}
        className="text-sm tabular-nums"
      />
    </label>
  )
}

// One stat-strip cell: a tiny uppercase label over its value.
function StatCell({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="bg-card px-3 py-2">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-sm font-medium tabular-nums">{children}</div>
    </div>
  )
}

// How a call is paid for, kept separate from who serves it: an enrolled login
// reads as its provider plus this chip, never as "Prowl login".
function CostChip({ tier, provider }: { tier: Tier; provider?: string }) {
  const { t } = useI18n()
  const tone = tier === 'subscription'
    ? 'bg-emerald-600/15 text-emerald-700 dark:text-emerald-400'
    : tier === 'paid'
      ? 'bg-amber-600/15 text-amber-700 dark:text-amber-400'
      : 'bg-muted text-muted-foreground'
  const title = tier === 'subscription' && provider ? t('catalog.subscriptionVia', { provider }) : undefined
  return <span title={title} className={`rounded-full px-1.5 py-0.5 text-[10px] ${tone}`}>{t(`catalog.cost_${tier}`)}</span>
}

function CapChip({ kind }: { kind: 'tools' | 'vision' | 'reasoning' }) {
  const { t } = useI18n()
  const tone = kind === 'vision'
    ? 'bg-cyan-600/15 text-cyan-700 dark:bg-cyan-400/15 dark:text-cyan-400'
    : kind === 'tools'
      ? 'bg-violet-600/15 text-violet-700 dark:bg-violet-400/15 dark:text-violet-400'
      : 'bg-fuchsia-600/15 text-fuchsia-700 dark:bg-fuchsia-400/15 dark:text-fuchsia-400'
  return <span className={`rounded-full px-1.5 py-0.5 text-[10px] ${tone}`}>{t(`catalog.cap_${kind}`)}</span>
}

function KeyChip({ status }: { status: KeyStatus }) {
  const { t } = useI18n()
  const tone = status === 'healthy'
    ? 'bg-emerald-600/15 text-emerald-700 dark:text-emerald-400'
    : status === 'error'
      ? 'bg-red-600/15 text-red-700 dark:text-red-400'
      : status === 'unknown'
        ? 'bg-amber-600/15 text-amber-700 dark:text-amber-400'
        : 'bg-muted text-muted-foreground'
  return <span className={`rounded-full px-1.5 py-0.5 text-[10px] ${tone}`}>{t(`catalog.key_${status}`)}</span>
}
