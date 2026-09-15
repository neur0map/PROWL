import { useEffect } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { X, ExternalLink, Plus, Trash2, Gauge, RefreshCw } from 'lucide-react'

import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { ConfirmButton } from '@/components/confirm-button'
import type { ApiKey } from '../../../../shared/types'
import { statusDot, statusLabelKey, type HealthData, type KeyActivity } from './shared'
import {
  CLASS_BY_KEY,
  META_CHIP,
  compactNumber,
  signupOf,
  type DirectoryProvider,
} from './provider-catalogue'
import { useI18n } from '@/i18n'

// One provider, everything about it: what it costs to join, what it serves,
// the credentials you hold for it, what the router did with each of them, and
// the add action. Previously those answers were spread across two tabs and a
// dialog, so "should I add a key here, and is the one I have working" could
// not be answered in one place.

interface ProbeResult {
  published: boolean
  message: string
  limit: number | null
  remaining: number | null
  window?: string
}

export function ProviderDrawer({
  provider,
  onClose,
  onAddKey,
  onAddCustomEndpoint,
}: {
  provider: DirectoryProvider | null
  onClose: () => void
  onAddKey: (platform: string) => void
  onAddCustomEndpoint: () => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()

  // Escape closes, which is what every other overlay in the dashboard does.
  useEffect(() => {
    if (!provider) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [provider, onClose])

  const { data: keys = [] } = useQuery<ApiKey[]>({
    queryKey: ['keys'],
    queryFn: () => apiFetch('/api/keys'),
    enabled: provider !== null,
  })
  const { data: health } = useQuery<HealthData>({
    queryKey: ['health'],
    queryFn: () => apiFetch('/api/health'),
    enabled: provider !== null,
  })
  const { data: activity } = useQuery<{ activity: KeyActivity[] }>({
    queryKey: ['keys-activity'],
    queryFn: () => apiFetch('/api/keys/activity'),
    enabled: provider !== null,
    refetchInterval: 15000,
  })

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['keys'] })
    void queryClient.invalidateQueries({ queryKey: ['health'] })
    void queryClient.invalidateQueries({ queryKey: ['providers-directory'] })
  }

  const setEnabled = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      apiFetch(`/api/keys/${id}`, { method: 'PATCH', body: JSON.stringify({ enabled }) }),
    onSuccess: invalidate,
  })
  const remove = useMutation({
    mutationFn: (id: number) => apiFetch(`/api/keys/${id}`, { method: 'DELETE' }),
    onSuccess: invalidate,
  })
  const check = useMutation({
    mutationFn: (id: number) => apiFetch(`/api/health/check/${id}`, { method: 'POST' }),
    onSuccess: invalidate,
  })
  const probe = useMutation({
    mutationFn: (id: string) => apiFetch<ProbeResult>(`/api/providers/${id}/probe`, { method: 'POST' }),
    meta: { silenceToast: true },
  })

  if (!provider) return null

  const cls = CLASS_BY_KEY[provider.class]
  const signup = signupOf(provider.friction)
  const mine = keys.filter(k => k.platform === provider.platform)
  const healthById = new Map((health?.keys ?? []).map(k => [k.id, k]))
  const activityById = new Map((activity?.activity ?? []).map(a => [a.keyId, a]))

  return (
    <div className="fixed inset-0 z-50 flex justify-end">
      <button
        type="button"
        aria-label={t('common.dismiss')}
        onClick={onClose}
        className="absolute inset-0 bg-background/70 backdrop-blur-sm"
      />
      <aside className="relative h-full w-full max-w-md overflow-y-auto border-l bg-card shadow-2xl">
        <div className="sticky top-0 z-10 flex items-start justify-between gap-3 border-b bg-card/95 px-5 py-4 backdrop-blur">
          <div className="min-w-0">
            <div className="flex items-center gap-2 min-w-0">
              <span className="size-2 rounded-sm shrink-0" style={{ background: cls.dot }} />
              <h2 className="text-base font-semibold truncate">{provider.name}</h2>
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-1.5">
              <span className="font-mono text-[11px] text-muted-foreground">{provider.id}</span>
              <span className={`${META_CHIP} bg-muted text-muted-foreground`}>{cls.label}</span>
              <span className={`${META_CHIP} ${signup.tone}`}>{signup.label}</span>
              {provider.keyless && (
                <span className={`${META_CHIP} bg-muted text-muted-foreground`}>no key needed</span>
              )}
              {!provider.adapter && (
                <span className={`${META_CHIP} bg-muted text-muted-foreground/70`}>custom only</span>
              )}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('common.dismiss')}
            className="-mr-1 rounded-lg p-1 text-muted-foreground/70 transition-colors hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>

        <div className="px-5 py-4 space-y-5">
          {provider.note && <p className="text-xs text-muted-foreground">{provider.note}</p>}

          <div className="grid grid-cols-3 gap-2">
            <Stat label="free models" value={provider.freeModels > 0 ? String(provider.freeModels) : '–'} />
            <Stat label="max context" value={provider.maxContext > 0 ? compactNumber(provider.maxContext) : '–'} />
            <Stat label="routable here" value={provider.modelCount > 0 ? String(provider.modelCount) : '–'} />
          </div>

          {provider.modalities.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {provider.modalities.map(m => (
                <span key={m} className={`${META_CHIP} bg-muted text-muted-foreground`}>{m}</span>
              ))}
            </div>
          )}

          <div className="flex flex-wrap items-center gap-2">
            {provider.adapter ? (
              <Button size="sm" onClick={() => onAddKey(provider.platform)}>
                <Plus className="size-3.5" />
                {provider.keyless ? 'Enable' : 'Add key'}
              </Button>
            ) : (
              <Button size="sm" variant="secondary" onClick={onAddCustomEndpoint}>
                <Plus className="size-3.5" />
                Add as custom endpoint
              </Button>
            )}
            {provider.configured && provider.probe !== 'none' && (
              <Button
                size="sm"
                variant="outline"
                disabled={probe.isPending}
                onClick={() => probe.mutate(provider.id)}
              >
                <Gauge className="size-3.5" />
                {probe.isPending ? 'checking' : 'Quota'}
              </Button>
            )}
            {(provider.apiKeyUrl || provider.docsUrl) && (
              <a
                href={provider.apiKeyUrl || provider.docsUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                {provider.apiKeyUrl ? 'Get a key' : 'Docs'}
                <ExternalLink className="size-3" />
              </a>
            )}
          </div>

          {probe.data && (
            <p className="text-xs text-muted-foreground">
              {probe.data.published && probe.data.remaining !== null
                ? `${compactNumber(probe.data.remaining)}${probe.data.limit ? ` of ${compactNumber(probe.data.limit)}` : ''} ${probe.data.window ?? ''} left`
                : probe.data.message}
            </p>
          )}

          <div>
            <h3 className="text-xs uppercase tracking-wide text-muted-foreground mb-2">
              Your keys {mine.length > 0 && <span className="tabular-nums">({mine.length})</span>}
            </h3>
            {mine.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                None yet. {provider.adapter
                  ? 'Add one above and the router starts using it immediately.'
                  : 'Point a custom endpoint at it to route here.'}
              </p>
            ) : (
              <ul className="space-y-1.5">
                {mine.map(k => {
                  const status = healthById.get(k.id)?.status ?? k.status
                  const act = activityById.get(k.id)
                  const reason = act?.lastErrorKind?.replace(/_/g, ' ')
                    || healthById.get(k.id)?.lastHealthError
                    || k.lastHealthError
                  return (
                    <li key={k.id} className="rounded-xl border bg-background/40 px-3 py-2">
                      <div className="flex items-center gap-2">
                        <span className={`size-1.5 rounded-full shrink-0 ${statusDot[status] ?? statusDot.unknown}`} />
                        <code className="font-mono text-[11px] truncate">{k.maskedKey}</code>
                        {k.label && <span className="text-[11px] text-muted-foreground truncate">{k.label}</span>}
                        <span className="text-[11px] text-muted-foreground ml-auto">
                          {statusLabelKey[status] ? t(statusLabelKey[status]) : status}
                        </span>
                        <Switch
                          size="sm"
                          checked={k.enabled}
                          onCheckedChange={enabled => setEnabled.mutate({ id: k.id, enabled })}
                          aria-label={t('keys.enable')}
                        />
                      </div>
                      <div className="mt-1 flex items-center gap-2 text-[11px] tabular-nums">
                        {act && act.served > 0 && <span className="text-emerald-500">{act.served} served</span>}
                        {act && act.hops > 0 && (
                          <span className="text-muted-foreground">
                            {act.hops} failed {act.hops === 1 ? 'hop' : 'hops'}
                          </span>
                        )}
                        {act?.coolingUntil != null && (
                          <span className="text-amber-400">cooling · {act.coolingModels}</span>
                        )}
                        {status !== 'healthy' && reason && (
                          <span className="text-rose-300/90 truncate" title={act?.lastError || reason}>
                            {reason}
                          </span>
                        )}
                        <span className="ml-auto flex items-center gap-1">
                          <button
                            type="button"
                            onClick={() => check.mutate(k.id)}
                            disabled={check.isPending}
                            className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground disabled:opacity-50"
                            title={t('keys.checkKey')}
                          >
                            <RefreshCw className="size-3" />
                          </button>
                          {/* Arm-then-confirm, the dashboard's destructive
                              idiom. A single click here would drop a working
                              credential with no way back. */}
                          <ConfirmButton
                            onConfirm={() => remove.mutate(k.id)}
                            disabled={remove.isPending}
                            size="icon-xs"
                            armedSize="xs"
                            className="size-5 text-muted-foreground hover:text-destructive"
                            title={t('common.delete')}
                            aria-label={t('common.delete')}
                          >
                            <Trash2 className="size-3" />
                          </ConfirmButton>
                        </span>
                      </div>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>

          {provider.sources.length > 0 && (
            <p className="text-[11px] text-muted-foreground/60">
              Listed by {provider.sources.join(', ')}.
            </p>
          )}
        </div>
      </aside>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border bg-background/40 px-3 py-2">
      <div className="text-sm font-semibold tabular-nums leading-tight">{value}</div>
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
    </div>
  )
}
