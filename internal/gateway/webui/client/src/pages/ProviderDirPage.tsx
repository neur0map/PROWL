import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Search, X } from 'lucide-react'

import { apiFetch } from '@/lib/api'
import { fetchProviders } from '@/lib/catalog'
import type { ProvidersResponse } from '@/lib/catalog'
import { PageHeader } from '@/components/page-header'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { ProviderDrawer } from '@/components/keys/provider-drawer'
import type { DirectoryProvider, DirectoryResponse } from '@/components/keys/provider-catalogue'
import { useI18n } from '@/i18n'
import { FilterRail } from '@/components/providers/filter-rail'
import { ProviderTable } from '@/components/providers/provider-table'
import { ProviderList } from '@/components/providers/provider-list'
import { toDirectoryProvider } from '@/components/providers/shared'
import {
  compareProviders,
  keyStateOf,
  signupKeyOf,
  DEFAULT_DIR,
  type AccessKey,
  type KeyStateKey,
  type SignupKey,
  type SortCol,
} from '@/components/providers/model'

const ACCESS_KEYS: AccessKey[] = ['free', 'paid', 'subscription']
const KEY_STATES: KeyStateKey[] = ['connected', 'failing', 'missing']
const SIGNUP_KEYS: SignupKey[] = ['card_required', 'card_none']
const SORT_COLS: SortCol[] = ['provider', 'access', 'key', 'models', 'routable', 'latency', 'reliability', 'quota', 'requests']

// First-paint row budget; a sentinel below the results streams in the rest as
// you scroll, so 200+ providers never render unbounded on load.
const RENDER_CHUNK = 50

function readSet<T extends string>(raw: string | null, allowed: T[]): Set<T> {
  const chosen = new Set((raw ?? '').split(',').filter(Boolean))
  return new Set(allowed.filter(k => chosen.has(k)))
}

export default function ProviderDirPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()

  const query = params.get('q') ?? ''
  const view = params.get('view') === 'list' ? 'list' : 'table'
  const rawAccess = params.get('access') ?? ''
  const rawKey = params.get('key') ?? ''
  const rawSignup = params.get('signup') ?? ''
  const sortColRaw = params.get('sort')
  const sortCol: SortCol = SORT_COLS.includes(sortColRaw as SortCol) ? (sortColRaw as SortCol) : 'models'
  const dirRaw = params.get('dir')
  const sortDir = dirRaw === 'asc' || dirRaw === 'desc' ? dirRaw : DEFAULT_DIR[sortCol]
  const drawerPlatform = params.get('drawer')

  const { data, isLoading, error } = useQuery<ProvidersResponse>({
    queryKey: ['catalog', 'providers'],
    queryFn: fetchProviders,
  })
  // Same query key as the Keys page catalogue, so the directory the drawer
  // needs is served from cache when either page has already loaded it. Only
  // fetched once a drawer is actually asked for; a catalogue row stands in
  // until it arrives.
  const { data: directory } = useQuery<DirectoryResponse>({
    queryKey: ['providers-directory'],
    queryFn: () => apiFetch('/api/providers/directory'),
    enabled: drawerPlatform !== null,
  })

  const access = readSet(rawAccess, ACCESS_KEYS)
  const keyState = readSet(rawKey, KEY_STATES)
  const signup = readSet(rawSignup, SIGNUP_KEYS)

  const { rows, accessCounts, keyCounts, signupCounts } = useMemo(() => {
    const providers = data?.providers ?? []
    const needle = query.trim().toLowerCase()
    const searchMatched = providers.filter(p =>
      !needle ||
      p.label.toLowerCase().includes(needle) ||
      p.platform.toLowerCase().includes(needle) ||
      p.source.toLowerCase().includes(needle))

    const ac: Record<AccessKey, number> = { free: 0, paid: 0, subscription: 0 }
    const kc: Record<KeyStateKey, number> = { connected: 0, failing: 0, missing: 0 }
    const sc: Record<SignupKey, number> = { card_required: 0, card_none: 0 }
    for (const p of searchMatched) {
      ac[p.access]++
      kc[keyStateOf(p)]++
      sc[signupKeyOf(p)]++
    }

    const accessSel = readSet(rawAccess, ACCESS_KEYS)
    const keySel = readSet(rawKey, KEY_STATES)
    const signupSel = readSet(rawSignup, SIGNUP_KEYS)
    const filtered = searchMatched.filter(p =>
      (accessSel.size === 0 || accessSel.has(p.access)) &&
      (keySel.size === 0 || keySel.has(keyStateOf(p))) &&
      (signupSel.size === 0 || signupSel.has(signupKeyOf(p))))
    const sorted = [...filtered].sort((a, b) => compareProviders(a, b, { col: sortCol, dir: sortDir }))
    return { rows: sorted, accessCounts: ac, keyCounts: kc, signupCounts: sc }
  }, [data, query, rawAccess, rawKey, rawSignup, sortCol, sortDir])

  const [renderLimit, setRenderLimit] = useState(RENDER_CHUNK)
  useEffect(() => {
    setRenderLimit(RENDER_CHUNK)
  }, [query, rawAccess, rawKey, rawSignup, sortCol, sortDir, view])
  const rendered = rows.slice(0, renderLimit)
  const hasMore = rows.length > renderLimit
  const sentinelRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!hasMore) return
    const el = sentinelRef.current
    if (!el) return
    const io = new IntersectionObserver(
      hits => {
        if (hits.some(h => h.isIntersecting)) setRenderLimit(l => l + RENDER_CHUNK)
      },
      { rootMargin: '400px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [hasMore])

  const dirByPlatform = useMemo(() => {
    const m = new Map<string, DirectoryProvider>()
    for (const dp of directory?.providers ?? []) {
      m.set(dp.platform, dp)
      m.set(dp.id, dp)
    }
    return m
  }, [directory])

  const drawerProvider = useMemo(() => {
    if (!drawerPlatform) return null
    const found = dirByPlatform.get(drawerPlatform)
    if (found) return found
    const row = (data?.providers ?? []).find(p => p.platform === drawerPlatform)
    return row ? toDirectoryProvider(row) : null
  }, [drawerPlatform, dirByPlatform, data])

  const setParam = (key: string, value: string, replace: boolean) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(next, { replace })
  }

  const toggleFacet = (key: 'access' | 'key' | 'signup', item: string) => {
    const cur = new Set((params.get(key) ?? '').split(',').filter(Boolean))
    if (cur.has(item)) cur.delete(item)
    else cur.add(item)
    setParam(key, [...cur].join(','), false)
  }

  const clearFilters = () => {
    const next = new URLSearchParams(params)
    for (const k of ['q', 'access', 'key', 'signup']) next.delete(k)
    setParams(next)
  }

  const drawerHref = (platform: string) => {
    const next = new URLSearchParams(params)
    next.set('drawer', platform)
    return `?${next.toString()}`
  }
  const modelsHref = (platform: string) => `/models?provider=${encodeURIComponent(platform)}`

  const goAddKey = (platform: string) => {
    setParam('drawer', '', true)
    navigate(`/keys?provider=${encodeURIComponent(platform)}`)
  }

  const viewOptions: { value: 'table' | 'list'; label: string }[] = [
    { value: 'table', label: t('catalog.view_table') },
    { value: 'list', label: t('catalog.view_list') },
  ]

  return (
    <div>
      <PageHeader
        title={t('providerDir.title')}
        description={t('providerDir.subtitle')}
        actions={
          <>
            <div className="relative w-full max-w-xs">
              <Search className="absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
              <Input
                value={query}
                onChange={e => setParam('q', e.target.value, true)}
                placeholder={t('providerDir.searchPlaceholder')}
                aria-label={t('providerDir.searchPlaceholder')}
                className="h-8 pl-8 text-xs"
              />
              {query && (
                <button
                  type="button"
                  onClick={() => setParam('q', '', true)}
                  aria-label={t('common.dismiss')}
                  className="absolute right-2 top-1/2 -translate-y-1/2 rounded text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <X className="size-3.5" />
                </button>
              )}
            </div>
            <SegmentedControl
              value={view}
              onValueChange={v => setParam('view', v === 'list' ? 'list' : '', false)}
              options={viewOptions}
              ariaLabel={t('providerDir.title')}
            />
          </>
        }
      />

      <div className="flex flex-col gap-6 md:flex-row">
        <FilterRail
          access={access}
          keyState={keyState}
          signup={signup}
          accessCounts={accessCounts}
          keyCounts={keyCounts}
          signupCounts={signupCounts}
          onToggleAccess={k => toggleFacet('access', k)}
          onToggleKey={k => toggleFacet('key', k)}
          onToggleSignup={k => toggleFacet('signup', k)}
          onClear={clearFilters}
        />

        <div className="min-w-0 flex-1">
          <p className="mb-3 text-xs text-muted-foreground tabular-nums">
            {t('providerDir.count', { shown: rows.length, total: data?.total ?? 0 })}
          </p>

          {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading')}</p>}
          {error && !isLoading && <p className="text-sm text-destructive">{t('common.error')}</p>}

          {!isLoading && !error && rows.length === 0 && (
            <div className="rounded-xl border py-12 text-center">
              <p className="text-sm text-muted-foreground">{t('providerDir.empty')}</p>
              <Button variant="outline" size="sm" className="mt-3" onClick={clearFilters}>
                {t('catalog.clear')}
              </Button>
            </div>
          )}

          {!isLoading && !error && rows.length > 0 && (
            <>
              {view === 'table' ? (
                <ProviderTable
                  rows={rendered}
                  sort={{ col: sortCol, dir: sortDir }}
                  onSort={col => {
                    const nextDir = sortCol === col ? (sortDir === 'asc' ? 'desc' : 'asc') : DEFAULT_DIR[col]
                    const next = new URLSearchParams(params)
                    next.set('sort', col)
                    next.set('dir', nextDir)
                    setParams(next)
                  }}
                  drawerHref={drawerHref}
                  modelsHref={modelsHref}
                />
              ) : (
                <ProviderList rows={rendered} drawerHref={drawerHref} modelsHref={modelsHref} />
              )}
              {hasMore && <div ref={sentinelRef} className="h-px" aria-hidden="true" />}
            </>
          )}
        </div>
      </div>

      <ProviderDrawer
        provider={drawerProvider}
        onClose={() => setParam('drawer', '', true)}
        onAddKey={goAddKey}
        onAddCustomEndpoint={() => goAddKey(drawerPlatform ?? '')}
      />
    </div>
  )
}
