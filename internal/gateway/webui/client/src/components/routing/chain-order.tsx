import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { Boxes } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useI18n } from '@/i18n'
import { apiFetch } from '@/lib/api'
import {
  buildGroups,
  type FallbackEntry,
  type ModelGroupRow,
  type RateLimitUsageData,
  type RoutingData,
  type RoutingStrategy,
  type Row,
} from '@/lib/routing'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/empty-state'
import { GroupHeaderCells, ModelTableHead, SortableGroupRow } from '@/components/model-table'
import { TableSkeleton } from '@/components/ui/skeleton'
import { FloatingBar } from '@/components/floating-bar'

// Rows rendered up front; a sentinel below the table streams in the rest as you
// scroll. Keeps first paint cheap when the chain grows into the hundreds
// without a virtualization dependency (which would fight dnd-kit).
const RENDER_CHUNK = 50

// The failover-order editor for the active chain: it orders (and enables) the
// models already turned on. Choosing WHICH models the gateway uses lives on the
// browse page now, so there is no catalogue filter or search here.
export function ChainOrder() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  // Staged edits carry the chain they were made against, so switching chains in
  // the manager above discards them instead of letting one chain's unsaved rows
  // be saved into another (#1021).
  const [staged, setStaged] = useState<{ profileId: number | null; entries: FallbackEntry[] } | null>(null)

  // The table edits the ACTIVE chain, so it is part of this query's identity
  // (#1021): keyed on 'fallback' alone, switching chains re-rendered the
  // previous chain's rows from cache, and a save then wrote them into the newly
  // activated one. Held back until the active chain is known so the first paint
  // is already the right chain's.
  const { data: active, isPending: activePending } = useQuery<{ activeProfileId: number | null }>({
    queryKey: ['profiles', 'active'],
    queryFn: () => apiFetch('/api/profiles/active'),
  })
  const activeProfileId = active?.activeProfileId ?? null

  const { data: entries = [], isLoading: entriesLoading } = useQuery<FallbackEntry[]>({
    queryKey: ['fallback', 'chain', activeProfileId],
    // The chain id rides in the request itself (#1047): keyed-but-unpinned, a
    // refetch racing an activation fetched "whichever chain is active by now"
    // into the OLD chain's cache entry, and switching A→B→A then rendered (and
    // could save) B's rows under A's name until a hard refresh.
    queryFn: () => apiFetch(activeProfileId != null ? `/api/fallback?profile=${activeProfileId}` : '/api/fallback'),
    enabled: !activePending,
  })
  const isLoading = activePending || entriesLoading

  // Staged edits are DISCARDED when the active chain changes, not just hidden
  // (#1047): merely masking them meant switching A→B→A resurrected the stale
  // unsaved rows over freshly fetched data, with only a refresh clearing them.
  useEffect(() => {
    setStaged(prev => (prev && prev.profileId !== activeProfileId ? null : prev))
  }, [activeProfileId])

  const localEntries = staged && staged.profileId === activeProfileId ? staged.entries : null
  const setLocalEntries = (next: FallbackEntry[] | null) =>
    setStaged(next === null ? null : { profileId: activeProfileId, entries: next })

  // Scores and the active strategy ride the same query the strategy card polls;
  // this observer reads them from that shared cache without a second poll timer.
  const { data: routing } = useQuery<RoutingData>({
    queryKey: ['fallback', 'routing'],
    queryFn: () => apiFetch('/api/fallback/routing'),
  })

  // Time-window rate-limit usage (#876). One observer and one poll timer for the
  // whole table — the row component reads it from a map instead of subscribing
  // per row, which on a large catalog was hundreds of observers and timers.
  const { data: rateLimitUsage } = useQuery<RateLimitUsageData>({
    queryKey: ['fallback', 'rate-limit-usage'],
    queryFn: () => apiFetch('/api/fallback/rate-limit-usage'),
    refetchInterval: 15_000,
  })
  const rateUsageByModel = useMemo(
    () => new Map((rateLimitUsage?.rows ?? []).map(r => [r.modelDbId, r])),
    [rateLimitUsage],
  )

  const saveMutation = useMutation({
    mutationFn: (data: { modelDbId: number; priority: number; enabled: boolean }[]) =>
      apiFetch('/api/fallback', { method: 'PUT', body: JSON.stringify(data) }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      setLocalEntries(null)
    },
  })

  const strategy: RoutingStrategy = routing?.strategy ?? 'balanced'
  const isManual = strategy === 'priority'

  // Merge fallback metadata with live scores, keyed by model. Memoized (#1047):
  // recomputing these over the chain on every render — once per landing query
  // plus once per 15s poll — was a large part of the "absurdly slow" feel.
  const scoreById = useMemo(
    () => new Map((routing?.scores ?? []).map(s => [s.modelDbId, s])),
    [routing?.scores],
  )
  const allEntries = useMemo(() => localEntries ?? entries, [localEntries, entries])
  const configured = useMemo(() => allEntries.filter(e => e.keyCount > 0), [allEntries])
  const unconfiguredPlatforms = useMemo(
    () => [...new Set(allEntries.filter(e => e.keyCount === 0).map(e => e.platform))],
    [allEntries],
  )

  // Entry fields win on overlap: the routing snapshot also carries `enabled`
  // (and identity fields), which would otherwise clobber unsaved local toggles.
  const rows: Row[] = useMemo(
    () => configured.map(e => ({ ...(scoreById.get(e.modelDbId) ?? {}), ...e })),
    [configured, scoreById],
  )

  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  function handleSave() {
    saveMutation.mutate(allEntries.map(e => ({ modelDbId: e.modelDbId, priority: e.priority, enabled: e.enabled })))
  }

  const hasChanges = localEntries !== null

  // A model served by several providers is shown as one logical row that links
  // to its own page; the on/off switch toggles every provider at once.
  const orderedGroups = useMemo(() => buildGroups(rows, isManual), [rows, isManual])
  const rankByKey = useMemo(() => new Map(orderedGroups.map((g, i) => [g.key, i + 1])), [orderedGroups])
  // Drag-to-reorder is only meaningful under the Manual strategy; the scoring
  // strategies decide order themselves.
  const draggable = isManual

  // Progressive rendering: grow the row budget whenever the sentinel below the
  // table scrolls near the viewport (drag autoscroll extends it too).
  const [renderLimit, setRenderLimit] = useState(RENDER_CHUNK)
  const renderedGroups = orderedGroups.slice(0, renderLimit)
  const hasMoreRows = orderedGroups.length > renderLimit
  const sentinelRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!hasMoreRows) return
    const el = sentinelRef.current
    if (!el) return
    const io = new IntersectionObserver(
      hits => {
        if (hits.some(h => h.isIntersecting)) setRenderLimit(l => l + RENDER_CHUNK)
      },
      { rootMargin: '600px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [hasMoreRows, renderLimit])

  function handleGroupToggle(memberIds: number[], enabled: boolean) {
    const ids = new Set(memberIds)
    setLocalEntries(allEntries.map(e => (ids.has(e.modelDbId) ? { ...e, enabled } : e)))
  }

  // Serialize the displayed group order (group-major, member-minor) to the flat
  // priority list PUT /api/fallback expects; keyless rows keep their tail spot.
  function persistGroupOrder(groups: ModelGroupRow[]) {
    const order: number[] = []
    for (const g of groups) for (const m of g.members) order.push(m.modelDbId)
    const unconfigured = allEntries.filter(e => e.keyCount === 0).map(e => e.modelDbId)
    const prio = new Map([...order, ...unconfigured].map((id, i) => [id, i + 1]))
    setLocalEntries(allEntries.map(e => ({ ...e, priority: prio.get(e.modelDbId) ?? e.priority })))
  }

  // Reorder models (the failover priority order). Providers within a model are
  // ordered by the active strategy and managed on the model's own page.
  function handleGroupedDragEnd(event: DragEndEvent) {
    const { active: dragged, over } = event
    if (!over || dragged.id === over.id) return
    const oldI = orderedGroups.findIndex(g => `grp:${g.key}` === String(dragged.id))
    const newI = orderedGroups.findIndex(g => `grp:${g.key}` === String(over.id))
    if (oldI < 0 || newI < 0) return
    persistGroupOrder(arrayMove(orderedGroups, oldI, newI))
  }

  if (isLoading) return <TableSkeleton rows={8} />

  // Nothing turned on: the browse page is where models get added to the gateway.
  if (orderedGroups.length === 0) {
    return (
      <EmptyState
        icon={Boxes}
        title={t('routing.emptyChain')}
        action={
          <Link to="/models">
            <Button size="sm">{t('routing.emptyChainCta')}</Button>
          </Link>
        }
      />
    )
  }

  return (
    <div className="space-y-3">
      {/* DndContext must wrap OUTSIDE the table: it renders hidden a11y
          live-region <div>s, which are invalid as direct <table> children. */}
      {draggable ? (
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleGroupedDragEnd}>
          <div className="rounded-2xl border overflow-x-auto">
            <table className="w-full text-sm">
              <ModelTableHead />
              <SortableContext items={renderedGroups.map(g => `grp:${g.key}`)} strategy={verticalListSortingStrategy}>
                <tbody>
                  {renderedGroups.map(g => (
                    <SortableGroupRow key={g.key} group={g} rank={rankByKey.get(g.key) ?? 0} onToggleGroup={handleGroupToggle} allRows={rows} rateUsage={rateUsageByModel} />
                  ))}
                </tbody>
              </SortableContext>
            </table>
          </div>
        </DndContext>
      ) : (
        <div className="rounded-2xl border overflow-x-auto">
          <table className="w-full text-sm">
            <ModelTableHead />
            <tbody>
              {renderedGroups.map(g => (
                <tr
                  key={g.key}
                  className={`group/row border-b last:border-0 transition-colors hover:[&>td]:bg-muted/50 [&>td:first-child]:rounded-l-lg [&>td:last-child]:rounded-r-lg ${g.members.some(m => m.enabled) ? '' : 'opacity-50'}`}
                >
                  <GroupHeaderCells group={g} rank={rankByKey.get(g.key) ?? 0} onToggleGroup={handleGroupToggle} allRows={rows} rateUsage={rateUsageByModel} />
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Invisible sentinel: when it nears the viewport the next row chunk
          renders. Present only while rows remain, so IO never fires idle. */}
      {hasMoreRows && <div ref={sentinelRef} className="h-px" aria-hidden="true" />}

      {/* Floating action bar — fixed to the viewport so it's always visible,
          sliding up when there are unsaved changes and back down on save/discard. */}
      <FloatingBar show={hasChanges}>
        <span className="text-xs text-muted-foreground">{t('common.unsavedChanges')}</span>
        <Button variant="outline" size="sm" onClick={() => setLocalEntries(null)}>{t('common.discard')}</Button>
        <Button size="sm" onClick={handleSave} disabled={saveMutation.isPending}>
          {saveMutation.isPending ? t('common.saving') : t('common.saveChanges')}
        </Button>
      </FloatingBar>

      {/* Members whose platform has no key are hidden; each name links to that
          provider's drawer, which is where a key is added. */}
      {unconfiguredPlatforms.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t('models.hiddenNoKeysPrefix')}{' '}
          {unconfiguredPlatforms.map((platform, i) => (
            <span key={platform}>
              {i > 0 && ', '}
              <Link to={`/keys?provider=${encodeURIComponent(platform)}`} className="underline decoration-dotted underline-offset-2 hover:text-foreground">
                {platform}
              </Link>
            </span>
          ))}
        </p>
      )}
    </div>
  )
}
