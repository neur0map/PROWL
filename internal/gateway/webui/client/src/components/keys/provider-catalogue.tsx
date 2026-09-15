import { useI18n } from '@/i18n'
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Search, CreditCard, Plus, Minus, X } from 'lucide-react'

import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

// The one provider surface: the whole catalogue, the operator's own keys, and
// the add flow in the same place. It replaced a split where the Keys tab
// listed what you had and a separate Providers tab listed what existed, so
// adding a key meant crossing between two pages that shared no vocabulary.
//
// The visual language is the model table's: dense single-line rows, a class
// dot instead of a filled badge, tiny meta chips, and a thin bar for a count
// so the column can be scanned rather than read.

export type ProviderClass = 'free' | 'credits' | 'paid' | 'oauth' | 'local'
type Friction = 'none' | 'registration' | 'phone' | 'card' | 'subscription'

export interface DirectoryProvider {
  id: string
  name: string
  class: ProviderClass
  friction: string
  freeModels: number
  maxContext: number
  modalities: string[]
  apiKeyUrl: string
  docsUrl: string
  env: string
  routable: boolean
  sources: string[]
  probe: string
  note?: string
  configured: boolean
  keyCount: number
  modelCount: number
  adapter: boolean
  platform: string
  keyless: boolean
}

export interface DirectoryCounts {
  free: number
  credits: number
  paid: number
  oauth: number
  local: number
  total: number
  configured: number
  freeModels: number
  routable: number
}

export interface DirectoryResponse {
  providers: DirectoryProvider[]
  counts: DirectoryCounts
  sources: Record<string, string>
}

interface LoginProvider {
  id: string
  name: string
  kind: string
  detail: string
  enrolled: boolean
}

export const CLASS_BY_KEY: Record<ProviderClass, { label: string; dot: string }> = {
  free: { label: 'Free', dot: '#22c55e' },
  credits: { label: 'Credits', dot: '#3b82f6' },
  local: { label: 'Local', dot: '#a855f7' },
  oauth: { label: 'Subscription', dot: '#f59e0b' },
  paid: { label: 'Paid', dot: '#6b7280' },
}

const CLASS_ORDER: ProviderClass[] = ['free', 'credits', 'local', 'oauth', 'paid']

// Only "card" is tinted: it is the one that can charge you. Tinting all five
// turns every row into a colour chart and none of them reads as a warning.
export const SIGNUP: Record<Friction, { label: string; tone: string }> = {
  none: { label: 'no signup', tone: 'bg-emerald-600/15 text-emerald-700 dark:bg-emerald-400/15 dark:text-emerald-400' },
  registration: { label: 'email', tone: 'bg-muted text-muted-foreground' },
  phone: { label: 'phone', tone: 'bg-muted text-muted-foreground' },
  card: { label: 'card', tone: 'bg-rose-600/15 text-rose-700 dark:bg-rose-400/15 dark:text-rose-400' },
  subscription: { label: 'subscription', tone: 'bg-muted text-muted-foreground' },
}

export const META_CHIP = 'text-[10px] rounded-full px-1.5 py-0.5 whitespace-nowrap'

export function signupOf(friction: string) {
  return SIGNUP[friction in SIGNUP ? (friction as Friction) : 'registration']
}

export function compactNumber(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value % 1_000_000 === 0 ? 0 : 1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(value % 1_000 === 0 ? 0 : 1)}K`
  return String(value)
}

function CountBar({ value, max, color }: { value: number; max: number; color: string }) {
  if (value <= 0) {
    return (
      <div className="flex items-center gap-1.5">
        <div className="h-1.5 w-12 rounded-full border border-dashed border-muted-foreground/30" />
        <span className="font-mono text-[11px] text-muted-foreground/60 tabular-nums w-7 text-right">–</span>
      </div>
    )
  }
  return (
    <div className="flex items-center gap-1.5">
      <div className="h-1.5 w-12 rounded-full bg-muted overflow-hidden">
        <div
          className="h-full rounded-full"
          style={{ width: `${Math.max(4, Math.round((value / Math.max(max, 1)) * 100))}%`, backgroundColor: color }}
        />
      </div>
      <span className="font-mono text-[11px] text-muted-foreground tabular-nums w-7 text-right">{value}</span>
    </div>
  )
}

// Logins Prowl already holds are spendable capacity the router could not
// otherwise reach. Enrolling one borrows the live credential rather than
// copying it, so a refresh follows and signing out revokes the pool's access.
function PoolLogins() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const { data } = useQuery({
    queryKey: ['gateway-logins'],
    queryFn: () => apiFetch<{ logins: LoginProvider[] }>('/api/logins'),
  })

  const enroll = useMutation({
    mutationFn: ({ id, on }: { id: string; on: boolean }) =>
      apiFetch(`/api/logins/${id}/enroll`, { method: on ? 'POST' : 'DELETE' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['gateway-logins'] })
      void queryClient.invalidateQueries({ queryKey: ['providers-directory'] })
      void queryClient.invalidateQueries({ queryKey: ['keys'] })
    },
  })

  const logins = data?.logins ?? []
  if (logins.length === 0) return null
  const inPool = logins.filter(l => l.enrolled).length

  return (
    <div className="rounded-2xl border bg-card p-4 mb-4">
      <div className="flex items-baseline justify-between gap-4">
        <h3 className="text-sm font-medium">{t('keys.loginsTitle')}</h3>
        <span className="text-[11px] text-muted-foreground tabular-nums">
          {t('keys.loginsCount', { inPool, total: logins.length })}
        </span>
      </div>
      <p className="text-[11px] text-muted-foreground mt-0.5 mb-3 max-w-2xl">
        {t('keys.loginsBlurb')}
      </p>
      <div className="flex flex-wrap gap-1.5">
        {logins.map(login => (
          <button
            key={login.id}
            type="button"
            disabled={enroll.isPending}
            onClick={() => enroll.mutate({ id: login.id, on: !login.enrolled })}
            title={login.detail || undefined}
            className={`inline-flex items-center gap-2 rounded-lg border px-2.5 py-1.5 text-xs transition-colors disabled:opacity-50 ${
              login.enrolled
                ? 'border-emerald-500/30 bg-emerald-500/10 text-foreground'
                : 'text-muted-foreground hover:text-foreground hover:bg-muted'
            }`}
          >
            {login.enrolled ? <Minus className="size-3" /> : <Plus className="size-3" />}
            {login.name}
            <span className="text-[10px] text-muted-foreground/70">
              {login.kind === 'oauth' ? 'subscription' : 'api key'}
            </span>
          </button>
        ))}
      </div>
    </div>
  )
}

export function ProviderCatalogue({ onOpenProvider }: { onOpenProvider: (p: DirectoryProvider) => void }) {
  const [query, setQuery] = useState('')
  const [classFilter, setClassFilter] = useState<ProviderClass | 'all'>('all')
  const [noCard, setNoCard] = useState(false)
  const [mineOnly, setMineOnly] = useState(false)

  const { data, isLoading, error } = useQuery<DirectoryResponse>({
    queryKey: ['providers-directory'],
    queryFn: () => apiFetch('/api/providers/directory'),
  })

  const rows = useMemo(() => {
    if (!data) return []
    const needle = query.trim().toLowerCase()
    return data.providers.filter(p => {
      if (classFilter !== 'all' && p.class !== classFilter) return false
      if (noCard && p.friction === 'card') return false
      if (mineOnly && !p.configured) return false
      if (!needle) return true
      return (
        p.name.toLowerCase().includes(needle) ||
        p.id.toLowerCase().includes(needle) ||
        p.modalities.some(m => m.toLowerCase().includes(needle)) ||
        p.sources.some(s => s.toLowerCase().includes(needle))
      )
    })
  }, [data, query, classFilter, noCard, mineOnly])

  // Bars scale to the widest row on screen, so filtering to one class
  // rescales instead of leaving every bar a stub.
  const maxFree = useMemo(() => rows.reduce((n, p) => Math.max(n, p.freeModels), 0), [rows])

  if (isLoading) return <div className="text-sm text-muted-foreground">Loading providers…</div>
  if (error || !data) return <div className="text-sm text-destructive">Could not load the provider directory.</div>

  const { counts } = data
  const noCardCount = data.providers.filter(p => p.friction !== 'card').length

  return (
    <div>
      <PoolLogins />

      <div className="flex flex-wrap items-center gap-2 mb-3">
        <div className="inline-flex items-center gap-1 rounded-xl border p-1" role="group" aria-label="Provider class">
          <button
            type="button"
            onClick={() => setClassFilter('all')}
            className={`px-2.5 py-1 text-xs rounded-lg transition-colors ${
              classFilter === 'all' ? 'bg-foreground text-background font-medium' : 'text-muted-foreground hover:text-foreground hover:bg-muted'
            }`}
          >
            All <span className="opacity-60 tabular-nums">{counts.total}</span>
          </button>
          {CLASS_ORDER.map(key => (
            <button
              key={key}
              type="button"
              onClick={() => setClassFilter(key)}
              className={`inline-flex items-center gap-1.5 px-2.5 py-1 text-xs rounded-lg transition-colors tabular-nums ${
                classFilter === key ? 'bg-foreground text-background font-medium' : 'text-muted-foreground hover:text-foreground hover:bg-muted'
              }`}
            >
              <span className="size-2 rounded-sm" style={{ background: CLASS_BY_KEY[key].dot }} />
              {CLASS_BY_KEY[key].label}
              <span className="opacity-60">{counts[key]}</span>
            </button>
          ))}
        </div>

        <button
          type="button"
          onClick={() => setNoCard(v => !v)}
          aria-pressed={noCard}
          title="Hide every provider that asks for a credit card"
          className={`inline-flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-lg border transition-colors ${
            noCard ? 'bg-foreground text-background border-foreground font-medium' : 'text-muted-foreground hover:text-foreground hover:bg-muted'
          }`}
        >
          <CreditCard className="size-3" />
          No card <span className="opacity-60 tabular-nums">{noCardCount}</span>
        </button>
        <button
          type="button"
          onClick={() => setMineOnly(v => !v)}
          aria-pressed={mineOnly}
          className={`px-3 py-1.5 text-xs rounded-lg border transition-colors ${
            mineOnly ? 'bg-foreground text-background border-foreground font-medium' : 'text-muted-foreground hover:text-foreground hover:bg-muted'
          }`}
        >
          Mine <span className="opacity-60 tabular-nums">{counts.configured}</span>
        </button>

        <div className="relative ml-auto w-full max-w-xs">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
          <Input
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder="Search providers…"
            className="pl-8 h-8 text-xs"
          />
          {query && (
            <button
              type="button"
              onClick={() => setQuery('')}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              aria-label="Clear search"
            >
              <X className="size-3.5" />
            </button>
          )}
        </div>
      </div>

      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-muted-foreground border-b">
            <th className="py-2 pl-3 pr-3 font-medium">Provider</th>
            <th className="py-2 pr-3 font-medium">Free models</th>
            <th className="py-2 pr-3 font-medium">Context</th>
            <th className="py-2 pr-3 font-medium text-right">Your keys</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(p => {
            const cls = CLASS_BY_KEY[p.class]
            const signup = signupOf(p.friction)
            return (
              <tr
                key={p.id}
                onClick={() => onOpenProvider(p)}
                className="group/row border-b last:border-0 bg-card cursor-pointer transition-colors hover:[&>td]:bg-muted/50 [&>td:first-child]:rounded-l-lg [&>td:last-child]:rounded-r-lg"
              >
                <td className="py-2 pl-3 pr-3 align-middle">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="size-2 rounded-sm shrink-0" style={{ background: cls.dot }} title={cls.label} />
                    <span className="font-medium truncate">{p.name}</span>
                    <span className="font-mono text-[11px] text-muted-foreground/70 truncate">{p.id}</span>
                    <span className={`${META_CHIP} ${signup.tone}`} title={`Signing up needs: ${signup.label}`}>
                      {signup.label}
                    </span>
                    {!p.adapter && (
                      <span
                        className={`${META_CHIP} bg-muted text-muted-foreground/70`}
                        title="No wire adapter yet — reachable as a custom endpoint"
                      >
                        custom only
                      </span>
                    )}
                    {p.note && (
                      <span className="text-[11px] text-muted-foreground/60 truncate hidden xl:inline" title={p.note}>
                        {p.note}
                      </span>
                    )}
                  </div>
                </td>
                <td className="py-2 pr-3 align-middle">
                  <CountBar value={p.freeModels} max={maxFree} color={cls.dot} />
                </td>
                <td className="py-2 pr-3 align-middle font-mono text-[11px] text-muted-foreground tabular-nums">
                  {p.maxContext > 0 ? compactNumber(p.maxContext) : '–'}
                </td>
                <td className="py-2 pr-3 align-middle text-right text-[11px] tabular-nums">
                  {p.configured ? (
                    <span className="text-emerald-500">
                      {p.keyCount} key{p.keyCount === 1 ? '' : 's'}
                      {p.modelCount > 0 && <span className="text-muted-foreground"> · {p.modelCount} models</span>}
                    </span>
                  ) : (
                    <span className="text-muted-foreground/60 opacity-0 transition-opacity group-hover/row:opacity-100">
                      add a key
                    </span>
                  )}
                </td>
              </tr>
            )
          })}
          {rows.length === 0 && (
            <tr>
              <td colSpan={4} className="py-10 text-center text-sm text-muted-foreground">
                No provider matches that filter.
                <Button
                  variant="outline"
                  size="sm"
                  className="ml-3"
                  onClick={() => {
                    setQuery('')
                    setClassFilter('all')
                    setNoCard(false)
                    setMineOnly(false)
                  }}
                >
                  Clear filters
                </Button>
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <p className="text-[11px] text-muted-foreground/70 mt-3">
        {rows.length} of {counts.total} shown · directory merged from {Object.keys(data.sources).join(', ')}.
      </p>
    </div>
  )
}
