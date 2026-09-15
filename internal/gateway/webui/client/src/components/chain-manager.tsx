import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Layers, Plus, Trash2 } from 'lucide-react'
import { useI18n } from '@/i18n'
import { apiFetch, type ApiError } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/tooltip'
import { CopyButton } from '@/components/copy-button'
import { ConfirmButton } from '@/components/confirm-button'

// Named fallback chains (#960/#895). The backend /api/profiles CRUD is
// complete and every chain is listed as an `auto:<name>` model in /v1/models;
// this panel is the dashboard surface for them: list chains, create new ones,
// switch the active chain (the order editor below then edits that chain), and
// delete custom ones. It is the Routing page's Lists section, rendered expanded
// rather than behind an accordion.

// A preset is a named query over the catalogue, counted live by the server.
// `group` sorts task presets ahead of catalogue ones; `requirements` is the
// plain-English list the server derives from the predicate, saying WHY.
interface ChainPreset {
  id: string
  name: string
  description: string
  group: 'task' | 'catalogue'
  requirements?: string[]
  models: number
}

export interface Chain {
  id: number
  name: string
  emoji: string
  color: string
  type: 'default' | 'builtin' | 'custom'
  is_favorite: number
  sort_order: number
  auto_sort: string | null
  layout_config: string | null
  // 0 once the chain opts out of the catalog-sync backfill (#895), which is
  // what an empty-created chain does — it stays exactly as hand-built.
  auto_include_new_models: number
  created_at: string
}

export function ChainManager() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [newName, setNewName] = useState('')
  const [startEmpty, setStartEmpty] = useState(true)
  const [createError, setCreateError] = useState('')

  const { data: chains = [] } = useQuery<Chain[]>({
    queryKey: ['profiles'],
    queryFn: () => apiFetch('/api/profiles'),
  })
  const { data: active } = useQuery<{ activeProfileId: number | null }>({
    queryKey: ['profiles', 'active'],
    queryFn: () => apiFetch('/api/profiles/active'),
  })
  const { data: presetData } = useQuery<{ presets: ChainPreset[] }>({
    queryKey: ['profiles', 'presets'],
    queryFn: () => apiFetch('/api/profiles/presets'),
  })

  const invalidate = async () => {
    // Resolve WHICH chain is active before refreshing anything keyed on it
    // (#1047): invalidating ['fallback'] first refetched the chain table under
    // the old profile id — a wasted full-catalog round trip, and the write that
    // poisoned the old chain's cache entry with the new chain's rows.
    await queryClient.refetchQueries({ queryKey: ['profiles', 'active'], exact: true })
    queryClient.invalidateQueries({ queryKey: ['profiles'] })
    // The fallback table renders the active chain; refresh it too. The prefix
    // covers the chain, routing, token-usage and rate-limit queries.
    queryClient.invalidateQueries({ queryKey: ['fallback'] })
  }

  const createChain = useMutation({
    mutationFn: (payload: { name: string; empty: boolean }) =>
      apiFetch('/api/profiles', { method: 'POST', body: JSON.stringify(payload) }),
    onSuccess: () => {
      invalidate()
      setNewName('')
      setCreateError('')
    },
    // Names are validated server-side (max 20 chars, Latin/digits/-/_ only, a
    // list of reserved words, no duplicates). Say which rule was broken rather
    // than swallowing the 400/409.
    onError: (error: ApiError) => setCreateError(error.message || t('chains.createFailed')),
  })
  // Building a set from a preset is one server call that fills and ranks it
  // from the same predicate it counted; `activate` follows noCustomActive.
  const createFromPreset = useMutation({
    mutationFn: (payload: { preset: ChainPreset; activate: boolean }) =>
      apiFetch(`/api/profiles/presets/${payload.preset.id}`, {
        method: 'POST',
        body: JSON.stringify({ name: payload.preset.name, activate: payload.activate }),
      }),
    onSuccess: () => {
      invalidate()
      setCreateError('')
    },
    onError: (error: ApiError) => setCreateError(error.message || t('chains.createFailed')),
  })
  const setActive = useMutation({
    mutationFn: (profileId: number) =>
      apiFetch('/api/profiles/active', { method: 'POST', body: JSON.stringify({ profileId }) }),
    onSuccess: invalidate,
  })
  const deleteChain = useMutation({
    mutationFn: (profileId: number) =>
      apiFetch(`/api/profiles/${profileId}`, { method: 'DELETE' }),
    onSuccess: invalidate,
  })

  if (chains.length === 0) return null

  const activeId = active?.activeProfileId ?? null
  const activeChain = chains.find(chain => chain.id === activeId)
  // Rule: a preset created here becomes active only when no custom set is
  // active yet (a default/builtin is); with one curated we add, no switch.
  const noCustomActive = !activeChain || activeChain.type !== 'custom'

  const presets = presetData?.presets ?? []
  const presetGroups = [
    { key: 'task', label: t('chains.presetGroup_task'), items: presets.filter(p => p.group === 'task') },
    { key: 'catalogue', label: t('chains.presetGroup_catalogue'), items: presets.filter(p => p.group !== 'task') },
  ]

  return (
    <section className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
        <div className="flex items-center gap-2">
          <Layers className="size-4 text-muted-foreground" />
          <div>
            <h2 className="text-sm font-medium">{t('chains.title')}</h2>
            <p className="text-xs text-muted-foreground">{t('chains.description')}</p>
          </div>
        </div>
        <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground tabular-nums">
          {t('chains.count', { count: chains.length })}
        </span>
      </div>

      <div className="p-4">
          <div className="space-y-2">
            {chains.map(chain => {
              const isActive = chain.id === activeId
              const isProtected = chain.type === 'default' || chain.type === 'builtin'
              return (
                <div
                  key={chain.id}
                  className={`flex flex-wrap items-center gap-2 rounded-xl border px-3 py-2 ${
                    isActive ? 'border-foreground/25 bg-muted/50' : 'border-transparent hover:bg-muted/40'
                  }`}
                >
                  <span className="flex items-center gap-1.5 text-sm font-medium" title={chain.name}>
                    {chain.emoji || <Layers className="size-3.5 text-muted-foreground" />}
                    <span className="truncate">{chain.name}</span>
                  </span>
                  {/* The id a client sends to pick this chain per request. */}
                  <span className="inline-flex items-center gap-1">
                    <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                      auto:{chain.name.toLowerCase()}
                    </code>
                    <CopyButton text={`auto:${chain.name.toLowerCase()}`} />
                  </span>
                  {isActive && (
                    <Badge variant="secondary" className="gap-1">
                      <Check className="size-3" />
                      {t('chains.active')}
                    </Badge>
                  )}
                  {isProtected && !isActive && (
                    <span className="text-[11px] text-muted-foreground">{t('chains.default')}</span>
                  )}
                  {chain.auto_include_new_models === 0 && (
                    <Tooltip text={t('chains.curatedHint')}>
                      <span className="cursor-help text-[11px] text-muted-foreground underline decoration-dotted underline-offset-2">
                        {t('chains.curated')}
                      </span>
                    </Tooltip>
                  )}
                  <span className="flex-1" />
                  {!isActive && (
                    <Tooltip text={t('chains.activateHint')}>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-7 px-2 text-xs"
                        disabled={setActive.isPending}
                        onClick={() => setActive.mutate(chain.id)}
                      >
                        {t('chains.activate')}
                      </Button>
                    </Tooltip>
                  )}
                  {!isProtected && (
                    <Tooltip text={t('chains.deleteHint')}>
                      <ConfirmButton
                        variant="ghost"
                        size="sm"
                        className="h-7 px-2 text-xs text-muted-foreground hover:text-rose-600"
                        aria-label={t('chains.deleteHint')}
                        disabled={deleteChain.isPending}
                        onConfirm={() => deleteChain.mutate(chain.id)}
                      >
                        <Trash2 className="size-3.5" />
                      </ConfirmButton>
                    </Tooltip>
                  )}
                </div>
              )
            })}
          </div>

          {presets.length > 0 && (
            <div className="mt-4 space-y-3">
              <p className="text-xs text-muted-foreground">{t('chains.presetsLabel')}</p>
              {presetGroups.map(group =>
                group.items.length === 0 ? null : (
                  <div key={group.key} className="space-y-1.5">
                    <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      {group.label}
                    </p>
                    <div className="grid gap-1.5 sm:grid-cols-2">
                      {group.items.map(preset => (
                        <Tooltip key={preset.id} text={preset.description}>
                          <button
                            type="button"
                            disabled={preset.models === 0 || createFromPreset.isPending}
                            onClick={() => createFromPreset.mutate({ preset, activate: noCustomActive })}
                            className="flex w-full flex-col items-start gap-1 rounded-xl border px-3 py-2 text-left transition-colors hover:bg-muted disabled:opacity-40 disabled:hover:bg-transparent"
                          >
                            <span className="flex w-full items-center gap-1.5 text-xs font-medium">
                              <Plus className="size-3 text-muted-foreground" />
                              <span className="truncate">{preset.name}</span>
                              <span className="ml-auto tabular-nums text-[11px] text-muted-foreground">
                                {preset.models}
                              </span>
                            </span>
                            {preset.requirements && preset.requirements.length > 0 && (
                              <span className="flex flex-wrap items-center gap-1">
                                <span className="text-[10px] text-muted-foreground">
                                  {t('chains.presetRequires')}
                                </span>
                                {preset.requirements.map(req => (
                                  <span
                                    key={req}
                                    className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground"
                                  >
                                    {req}
                                  </span>
                                ))}
                              </span>
                            )}
                          </button>
                        </Tooltip>
                      ))}
                    </div>
                  </div>
                ),
              )}
            </div>
          )}

          <form
            className="mt-3 flex gap-2"
            onSubmit={e => {
              e.preventDefault()
              const name = newName.trim()
              if (name && !createChain.isPending) createChain.mutate({ name, empty: startEmpty })
            }}
          >
            <Input
              value={newName}
              onChange={e => {
                setNewName(e.target.value)
                setCreateError('')
              }}
              placeholder={t('chains.createPlaceholder')}
              aria-label={t('chains.createPlaceholder')}
              className="h-8"
            />
            <Button type="submit" size="sm" className="h-8" disabled={!newName.trim() || createChain.isPending}>
              <Plus className="size-3.5" />
              {t('chains.create')}
            </Button>
          </form>
          <label className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={startEmpty}
              onChange={e => setStartEmpty(e.target.checked)}
              className="size-3.5 accent-foreground"
            />
            <span>{t('chains.startEmpty')}</span>
            <Tooltip text={t('chains.startEmptyHint')}>
              <span className="cursor-help underline decoration-dotted underline-offset-2">?</span>
            </Tooltip>
          </label>

          {createError
            ? <p className="mt-1.5 text-xs text-rose-600 dark:text-rose-400">{createError}</p>
            : <p className="mt-1.5 text-xs text-muted-foreground">{t('chains.nameRules')}</p>}
        </div>
    </section>
  )
}
