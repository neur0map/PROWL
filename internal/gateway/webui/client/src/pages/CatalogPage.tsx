import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Search, SlidersHorizontal, X } from 'lucide-react'
import { useI18n } from '@/i18n'
import {
  allInGateway,
  allRowIds,
  fetchCatalog,
  noneInGateway,
  routableRowIds,
  setModelsInGateway,
  type CatalogModel,
  type CatalogResponse,
  type Modality,
} from '@/lib/catalog'
import { PageHeader } from '@/components/page-header'
import { EmptyState } from '@/components/empty-state'
import { TableSkeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { FloatingBar } from '@/components/floating-bar'
import { FilterRail, type CatalogFilters, type FilterGroupKey } from '@/components/catalog/filter-rail'
import { ModelRow } from '@/components/catalog/model-row'
import { CatalogTable, TABLE_COLS, type TableSort, type TableSortCol } from '@/components/catalog/catalog-table'
import { ConfirmButton } from '@/components/confirm-button'
import { Tooltip } from '@/components/tooltip'
import { SetBar } from '@/components/catalog/set-bar'
import { SaveSetDialog } from '@/components/catalog/save-set-dialog'

const MODALITIES: Modality[] = ['chat', 'embeddings', 'image', 'video', 'audio']
const SORTS = ['score', 'price', 'context', 'speed', 'name'] as const
type SortKey = typeof SORTS[number]
const CONTEXT_BUCKETS = [32000, 128000, 1000000]

// First paint renders this many rows; the sentinel below the results grows the
// budget as it scrolls into view, so a 400-model list never renders at once.
const RENDER_CHUNK = 50

// Each filter group's URL query-param name. The value is a comma-joined list of
// the group's selected option keys, so a filtered view is fully linkable.
const FILTER_PARAMS: Record<FilterGroupKey, string> = {
  avail: 'avail',
  tier: 'tier',
  caps: 'caps',
  context: 'ctx',
  providers: 'provider',
  authors: 'author',
}

function parseModality(value: string | null): Modality {
  return MODALITIES.includes(value as Modality) ? (value as Modality) : 'chat'
}

function parseSort(value: string | null): SortKey {
  return SORTS.includes(value as SortKey) ? (value as SortKey) : 'score'
}

function parseTableSort(value: string | null): TableSort {
  const [col, dir] = (value ?? '').split(':')
  return {
    col: TABLE_COLS.includes(col as TableSortCol) ? (col as TableSortCol) : 'score',
    dir: dir === 'asc' ? 'asc' : 'desc',
  }
}

function splitParam(value: string | null): string[] {
  return value ? value.split(',').filter(Boolean) : []
}

function matchesFilters(m: CatalogModel, f: CatalogFilters, query: string): boolean {
  if (f.avail.size) {
    const ok =
      (f.avail.has('inGateway') && m.inGateway) ||
      (f.avail.has('routable') && m.routable) ||
      (f.avail.has('needsKey') && !m.routable)
    if (!ok) return false
  }
  if (f.tier.size && !f.tier.has(m.tier)) return false
  // Capabilities are AND: "can do tools and vision" means both, not either.
  if (f.caps.has('tools') && !m.caps.tools) return false
  if (f.caps.has('vision') && !m.caps.vision) return false
  if (f.caps.has('reasoning') && !m.caps.reasoning) return false
  if (f.context.size) {
    let min = Infinity
    for (const v of f.context) min = Math.min(min, Number(v))
    if ((m.contextWindow ?? 0) < min) return false
  }
  if (f.providers.size && !m.providers.some(p => f.providers.has(p.platform))) return false
  if (f.authors.size && !f.authors.has(m.author)) return false
  if (query) {
    const hay = `${m.name} ${m.author} ${m.canonicalId} ${m.providers.map(p => `${p.platform} ${p.label}`).join(' ')}`.toLowerCase()
    if (!hay.includes(query)) return false
  }
  return true
}

// Cheapest first: free and subscription models cost nothing at the margin, a
// priced model ranks by its output price (the number that dominates a bill),
// and a paid model with no published price sinks to the bottom.
function priceSortKey(m: CatalogModel): number {
  if (m.priceOut != null) return m.priceOut
  return m.tier === 'paid' ? Infinity : 0
}

function sortForList(models: CatalogModel[], key: SortKey): CatalogModel[] {
  const arr = [...models]
  switch (key) {
    case 'score': arr.sort((a, b) => (b.score ?? -1) - (a.score ?? -1)); break
    case 'price': arr.sort((a, b) => priceSortKey(a) - priceSortKey(b)); break
    case 'context': arr.sort((a, b) => (b.contextWindow ?? -1) - (a.contextWindow ?? -1)); break
    case 'speed': arr.sort((a, b) => (b.speed ?? -1) - (a.speed ?? -1)); break
    case 'name': arr.sort((a, b) => a.name.localeCompare(b.name)); break
  }
  return arr
}

function tableValue(m: CatalogModel, col: TableSortCol): number | string | null {
  switch (col) {
    case 'providers': return m.providers.length
    case 'name': return m.name
    case 'context': return m.contextWindow
    case 'in': return m.priceIn
    case 'out': return m.priceOut
    case 'reliability': return m.reliability
    case 'speed': return m.speed
    case 'score': return m.score
  }
}

function sortForTable(models: CatalogModel[], sort: TableSort): CatalogModel[] {
  const dir = sort.dir === 'asc' ? 1 : -1
  return [...models].sort((a, b) => {
    const va = tableValue(a, sort.col)
    const vb = tableValue(b, sort.col)
    if (typeof va === 'string' || typeof vb === 'string') return String(va).localeCompare(String(vb)) * dir
    // A missing number always sinks, whichever way the column is sorted.
    if (va == null && vb == null) return 0
    if (va == null) return 1
    if (vb == null) return -1
    return (va - vb) * dir
  })
}

function segButton(active: boolean): string {
  return `inline-flex items-center gap-1 rounded-lg px-3 py-1.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-40 ${
    active ? 'bg-foreground text-background font-medium' : 'text-muted-foreground hover:bg-muted hover:text-foreground'
  }`
}

// One button of the filtered bulk pair. A large sweep is destructive enough to
// arm-and-confirm; a small one fires on the first click.
function BulkButton({ label, confirm, destructive, disabled, onFire }: {
  label: string
  confirm: boolean
  destructive?: boolean
  disabled: boolean
  onFire: () => void
}) {
  const className = destructive ? 'text-muted-foreground hover:text-rose-600' : undefined
  return confirm ? (
    <ConfirmButton variant="ghost" size="sm" className={className} disabled={disabled} onConfirm={onFire}>{label}</ConfirmButton>
  ) : (
    <Button variant="ghost" size="sm" className={className} disabled={disabled} onClick={onFire}>{label}</Button>
  )
}

export default function CatalogPage() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()

  const modality = parseModality(searchParams.get('modality'))
  const view = searchParams.get('view') === 'table' ? 'table' : 'list'
  const sort = parseSort(searchParams.get('sort'))
  const tableSort = useMemo(() => parseTableSort(searchParams.get('tsort')), [searchParams])
  const collapsed = useMemo(() => new Set(splitParam(searchParams.get('closed'))), [searchParams])
  const filters = useMemo<CatalogFilters>(() => ({
    avail: new Set(splitParam(searchParams.get('avail'))),
    tier: new Set(splitParam(searchParams.get('tier'))),
    caps: new Set(splitParam(searchParams.get('caps'))),
    context: new Set(splitParam(searchParams.get('ctx'))),
    providers: new Set(splitParam(searchParams.get('provider'))),
    authors: new Set(splitParam(searchParams.get('author'))),
  }), [searchParams])

  const [search, setSearch] = useState(() => searchParams.get('q') ?? '')
  const [selected, setSelected] = useState<Set<number>>(() => new Set())
  const [railOpen, setRailOpen] = useState(false)
  const [renderLimit, setRenderLimit] = useState(RENDER_CHUNK)
  const [saveOpen, setSaveOpen] = useState(false)

  // Make the active modality shareable by writing it into the URL when a deep
  // link omitted it. The guard means this fires once and never loops.
  useEffect(() => {
    if (searchParams.get('modality')) return
    const next = new URLSearchParams(searchParams)
    next.set('modality', modality)
    setSearchParams(next, { replace: true })
  }, [searchParams, modality, setSearchParams])

  const { data } = useQuery({
    queryKey: ['catalog', modality],
    queryFn: () => fetchCatalog(modality),
    placeholderData: keepPreviousData,
  })
  const models = useMemo(() => data?.models ?? [], [data])
  const facets = data?.facets

  const gatewayMutation = useMutation({
    // On = add only rows a key can serve; off = remove every row, keyless ones
    // included, or a keyless chain member keeps the model in the gateway.
    mutationFn: ({ targets, use }: { targets: CatalogModel[]; use: boolean }) =>
      setModelsInGateway(targets.flatMap(use ? routableRowIds : allRowIds), use),
    onMutate: async ({ targets, use }) => {
      await queryClient.cancelQueries({ queryKey: ['catalog', modality] })
      const prev = queryClient.getQueryData<CatalogResponse>(['catalog', modality])
      if (prev) {
        const ids = new Set(targets.map(m => m.id))
        queryClient.setQueryData<CatalogResponse>(['catalog', modality], {
          ...prev,
          models: prev.models.map(m => (ids.has(m.id) ? { ...m, inGateway: use } : m)),
        })
      }
      return { prev }
    },
    onError: (_error, _vars, ctx) => {
      if (ctx?.prev) queryClient.setQueryData(['catalog', modality], ctx.prev)
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['catalog'] })
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      queryClient.invalidateQueries({ queryKey: ['profiles'] })
    },
  })

  const query = search.trim().toLowerCase()
  const filtered = useMemo(() => models.filter(m => matchesFilters(m, filters, query)), [models, filters, query])
  const ordered = useMemo(
    () => (view === 'table' ? sortForTable(filtered, tableSort) : sortForList(filtered, sort)),
    [filtered, view, tableSort, sort],
  )
  const rendered = ordered.slice(0, renderLimit)
  const hasMore = ordered.length > renderLimit
  const selectedModels = useMemo(() => models.filter(m => selected.has(m.id)), [models, selected])
  // The header checkbox reflects the rows on screen, so it reads "checked" only
  // when every rendered row is already picked.
  const allShownSelected = rendered.length > 0 && rendered.every(m => selected.has(m.id))

  const contextCounts = useMemo(() => {
    const counts: Record<number, number> = { 32000: 0, 128000: 0, 1000000: 0 }
    for (const m of models) {
      const c = m.contextWindow ?? 0
      for (const b of CONTEXT_BUCKETS) if (c >= b) counts[b]++
    }
    return counts
  }, [models])

  const sentinelRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!hasMore) return
    const el = sentinelRef.current
    if (!el) return
    const io = new IntersectionObserver(
      hits => { if (hits.some(h => h.isIntersecting)) setRenderLimit(l => l + RENDER_CHUNK) },
      { rootMargin: '600px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [hasMore, renderLimit])

  const activeFilterCount =
    filters.avail.size + filters.tier.size + filters.caps.size +
    filters.context.size + filters.providers.size + filters.authors.size
  const hasActive = query !== '' || activeFilterCount > 0

  function updateParams(mutate: (p: URLSearchParams) => void, opts?: { replace?: boolean }) {
    const next = new URLSearchParams(searchParams)
    mutate(next)
    setSearchParams(next, { replace: opts?.replace })
  }

  function changeModality(next: Modality) {
    setSelected(new Set())
    setRenderLimit(RENDER_CHUNK)
    updateParams(p => p.set('modality', next))
  }

  function changeSearch(value: string) {
    setSearch(value)
    updateParams(p => (value ? p.set('q', value) : p.delete('q')), { replace: true })
  }

  function toggleFilter(group: FilterGroupKey, value: string) {
    const next = new Set(filters[group])
    if (next.has(value)) next.delete(value)
    else next.add(value)
    updateParams(p => (next.size ? p.set(FILTER_PARAMS[group], [...next].join(',')) : p.delete(FILTER_PARAMS[group])))
  }

  function toggleSection(key: string) {
    const next = new Set(collapsed)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    updateParams(p => (next.size ? p.set('closed', [...next].join(',')) : p.delete('closed')))
  }

  function changeTableSort(col: TableSortCol) {
    const dir = tableSort.col === col ? (tableSort.dir === 'asc' ? 'desc' : 'asc') : col === 'name' ? 'asc' : 'desc'
    updateParams(p => p.set('tsort', `${col}:${dir}`))
  }

  function clearFilters() {
    setSearch('')
    updateParams(p => { for (const k of ['avail', 'tier', 'caps', 'ctx', 'provider', 'author', 'q']) p.delete(k) })
  }

  function toggleSelect(id: number, on: boolean) {
    setSelected(prev => {
      const next = new Set(prev)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }

  function bulkUse(use: boolean) {
    gatewayMutation.mutate({ targets: selectedModels, use })
    setSelected(new Set())
  }

  // Act on the whole filtered set, not the selection: the filters ARE the
  // scope, so "free + tool calling + 128K" then Use all seats exactly those.
  function bulkFiltered(use: boolean) {
    gatewayMutation.mutate({ targets: filtered, use })
  }

  function toggleSelectAllShown(on: boolean) {
    setSelected(prev => {
      if (!on) return new Set()
      const next = new Set(prev)
      for (const m of rendered) next.add(m.id)
      return next
    })
  }

  return (
    <div>
      <PageHeader title={t('catalog.title')} description={t('catalog.subtitle')} />

      {!data || !facets ? (
        <TableSkeleton rows={10} />
      ) : (
        <>
          <div className="mb-4 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <div className="flex flex-1 items-center gap-2">
              <button
                type="button"
                onClick={() => setRailOpen(o => !o)}
                aria-expanded={railOpen}
                className="inline-flex items-center gap-1.5 rounded-xl border px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 lg:hidden"
              >
                <SlidersHorizontal className="size-4" />
                {t('catalog.filters')}
                {activeFilterCount > 0 && (
                  <span className="rounded-full bg-foreground px-1.5 text-[10px] tabular-nums text-background">{activeFilterCount}</span>
                )}
              </button>
              <div className="relative flex-1 lg:max-w-xs">
                <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                <input
                  value={search}
                  onChange={e => changeSearch(e.target.value)}
                  placeholder={t('catalog.searchPlaceholder')}
                  aria-label={t('catalog.searchPlaceholder')}
                  className="w-full rounded-xl border bg-card py-1.5 pl-9 pr-8 text-sm outline-none transition-colors focus:border-foreground/30"
                />
                {search && (
                  <button
                    type="button"
                    onClick={() => changeSearch('')}
                    aria-label={t('models.clearSearch')}
                    className="absolute right-2 top-1/2 -translate-y-1/2 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  >
                    <X className="size-4" />
                  </button>
                )}
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                <span>{t('catalog.sortBy')}</span>
                <select
                  value={sort}
                  onChange={e => updateParams(p => (e.target.value === 'score' ? p.delete('sort') : p.set('sort', e.target.value)))}
                  className="rounded-lg border bg-background px-2 py-1.5 text-xs text-foreground outline-none focus-visible:border-foreground/30"
                >
                  {SORTS.map(s => <option key={s} value={s}>{t(`catalog.sort_${s}`)}</option>)}
                </select>
              </label>
              <div className="inline-flex items-center gap-1 rounded-xl border p-1" role="group">
                <button type="button" onClick={() => updateParams(p => p.delete('view'))} aria-pressed={view === 'list'} className={segButton(view === 'list')}>{t('catalog.view_list')}</button>
                <button type="button" onClick={() => updateParams(p => p.set('view', 'table'))} aria-pressed={view === 'table'} className={segButton(view === 'table')}>{t('catalog.view_table')}</button>
              </div>
              <span className="text-xs tabular-nums text-muted-foreground">{t('catalog.count', { shown: filtered.length, total: data.total })}</span>
            </div>
          </div>

          <div className="mb-4 inline-flex flex-wrap items-center gap-1 rounded-xl border p-1" role="group">
            {MODALITIES.map(mod => (
              <button
                key={mod}
                type="button"
                onClick={() => changeModality(mod)}
                aria-pressed={modality === mod}
                disabled={facets.modalities[mod] === 0}
                title={facets.modalities[mod] === 0 ? t('catalog.modalityEmpty', { modality: t(`catalog.modality_${mod}`) }) : undefined}
                className={segButton(modality === mod)}
              >
                {t(`catalog.modality_${mod}`)}
                <span className="tabular-nums opacity-60">{facets.modalities[mod]}</span>
              </button>
            ))}
          </div>

          <div className="flex flex-col gap-6 lg:flex-row">
            <aside className={`${railOpen ? 'block' : 'hidden'} lg:block lg:w-60 lg:shrink-0`}>
              <div className="lg:sticky lg:top-4 lg:max-h-[calc(100vh-2rem)] lg:overflow-y-auto lg:pr-1">
                <FilterRail
                  facets={facets}
                  contextCounts={contextCounts}
                  selected={filters}
                  collapsed={collapsed}
                  hasActive={hasActive}
                  onToggle={toggleFilter}
                  onToggleSection={toggleSection}
                  onClear={clearFilters}
                />
              </div>
            </aside>

            <div className="min-w-0 flex-1">
              <SetBar />
              {filtered.length > 0 && (
                <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
                  <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                    <input
                      type="checkbox"
                      checked={allShownSelected}
                      onChange={e => toggleSelectAllShown(e.target.checked)}
                      aria-label={allShownSelected ? t('catalog.clearSelection') : t('catalog.selectAllShown')}
                      className="size-3.5 accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    />
                    <span>{allShownSelected ? t('catalog.clearSelection') : t('catalog.selectAllShown')}</span>
                  </label>
                  <Tooltip text={t('catalog.bulkHint', { count: filtered.length })} side="bottom">
                    <div className="inline-flex items-center gap-1 rounded-xl border p-1" role="group">
                      <BulkButton
                        label={t('catalog.useAll', { count: filtered.length })}
                        confirm={filtered.length > 25}
                        disabled={gatewayMutation.isPending || allInGateway(filtered)}
                        onFire={() => bulkFiltered(true)}
                      />
                      <BulkButton
                        label={t('catalog.removeAll', { count: filtered.length })}
                        confirm={filtered.length > 25}
                        destructive
                        disabled={gatewayMutation.isPending || noneInGateway(filtered)}
                        onFire={() => bulkFiltered(false)}
                      />
                    </div>
                  </Tooltip>
                </div>
              )}
              {filtered.length === 0 ? (
                <EmptyState
                  title={t('catalog.empty')}
                  description={t('catalog.emptyHint')}
                  action={hasActive ? <Button variant="outline" size="sm" onClick={clearFilters}>{t('catalog.clear')}</Button> : undefined}
                />
              ) : view === 'table' ? (
                <CatalogTable
                  models={rendered}
                  modality={modality}
                  selection={selected}
                  sort={tableSort}
                  onSelect={toggleSelect}
                  onUse={(m, use) => gatewayMutation.mutate({ targets: [m], use })}
                  onSort={changeTableSort}
                />
              ) : (
                <div className="overflow-hidden rounded-2xl border">
                  {rendered.map(m => (
                    <ModelRow
                      key={m.id}
                      model={m}
                      modality={modality}
                      selected={selected.has(m.id)}
                      onSelect={toggleSelect}
                      onUse={(model, use) => gatewayMutation.mutate({ targets: [model], use })}
                    />
                  ))}
                </div>
              )}
              {hasMore && <div ref={sentinelRef} className="h-px" aria-hidden="true" />}
            </div>
          </div>

          <FloatingBar show={selected.size > 0}>
            <span className="text-xs tabular-nums text-muted-foreground">{t('catalog.selected', { count: selected.size })}</span>
            <Button variant="outline" size="sm" onClick={() => setSelected(new Set())}>{t('common.cancel')}</Button>
            <Button variant="outline" size="sm" onClick={() => setSaveOpen(true)} disabled={gatewayMutation.isPending}>{t('catalog.saveAsSet')}</Button>
            <Button variant="outline" size="sm" onClick={() => bulkUse(false)} disabled={gatewayMutation.isPending}>{t('catalog.dropSelected')}</Button>
            <Button size="sm" onClick={() => bulkUse(true)} disabled={gatewayMutation.isPending}>{t('catalog.useSelected')}</Button>
          </FloatingBar>

          {saveOpen && (
            <SaveSetDialog
              models={selectedModels}
              onClose={() => setSaveOpen(false)}
              onSaved={() => { setSaveOpen(false); setSelected(new Set()) }}
            />
          )}
        </>
      )}
    </div>
  )
}
